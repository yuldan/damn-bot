// Package tools implements the tool dispatch pipeline of SPEC §4.3: lookup,
// argument validation (internal/schema), policy and approval classes, the
// journal transaction boundary, Run, redaction, spill and result rendering.
//
// The package is the consumer of the eager tool implementations: file tools
// live in internal/codetools, shell in internal/exec, classification in
// internal/exec/classify (ENGINEERING E-09). Every path the pipeline checks
// goes through internal/sandbox; shell is not a path boundary (ADR-079).
package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/damnbot/damnbot/internal/schema"
)

// Effect is a tool call's effect class (SPEC §4.3): it decides parallelism,
// recovery and undo. Dynamic tools (shell, MCP) resolve the class per call.
type Effect string

// Effect classes.
const (
	EffectRead          Effect = "read"
	EffectIdempotent    Effect = "idempotent"
	EffectNonIdempotent Effect = "non_idempotent"
	effectDynamic       Effect = "dynamic"
)

// Kind is the ACP ToolKind of a tool.
type Kind string

// Tool kinds.
const (
	KindRead    Kind = "read"
	KindEdit    Kind = "edit"
	KindDelete  Kind = "delete"
	KindMove    Kind = "move"
	KindSearch  Kind = "search"
	KindExecute Kind = "execute"
	KindThink   Kind = "think"
	KindFetch   Kind = "fetch"
	KindOther   Kind = "other"
)

// Tool is one eager tool (SPEC §4.3). Schema returns the model-facing
// document {"name","description","parameters"} whose parameters object is a
// subset of SPEC §4.4 with additionalProperties:false.
type Tool interface {
	Name() string
	Schema() json.RawMessage
	Kind() Kind
	Effect(args json.RawMessage) Effect
	Run(ctx context.Context, env Env, args json.RawMessage) (Result, error)
}

// BlobRef points at one content-addressed blob (SPEC §11.6).
type BlobRef struct {
	SHA  string
	MIME string
	Size int64
	Path string
}

// Result is a successful tool result (SPEC §4.3). Content is the
// model-facing text before redaction, paging and spill.
type Result struct {
	OK          bool
	Content     string
	Err         *ToolError
	Meta        map[string]any
	Attachments []BlobRef
	// Spilled is the blob holding the full result when the tool spilled it
	// internally (shell exec summary); dispatcher promotes it to the event.
	Spilled *BlobRef

	// UntrustedSource, when non-empty, wraps Content in
	// <untrusted source="…"> and taints the turn (SPEC §14.5, ADR-070).
	UntrustedSource string
}

// Env carries dispatcher facts into a tool. Approved reports that the user
// approved the call's outside_root/hardlinked verdict. PageChars is the
// per-call page cap (ADR-031); InlineLeft is what remains of the turn's
// inline budget (P5).
type Env struct {
	TurnID     string
	CallID     string
	Cwd        string
	Approved   bool
	PageChars  int
	InlineLeft int
}

// Code is one value of the closed SPEC §4.3 error-code set.
type Code string

// The closed error-code set (SPEC §4.3). Codes outside this set are
// forbidden (TestErrorCodesClosed).
const (
	CodeUnknownTool         Code = "unknown_tool"
	CodeNotLoaded           Code = "not_loaded"
	CodeInvalidArgs         Code = "invalid_args"
	CodeNotFound            Code = "not_found"
	CodeOutsideWorkspace    Code = "outside_workspace"
	CodeDeniedPath          Code = "denied_path"
	CodeDenied              Code = "denied"
	CodeHookDenied          Code = "hook_denied"
	CodeTimeout             Code = "timeout"
	CodeTooLarge            Code = "too_large"
	CodeExecFailed          Code = "exec_failed"
	CodeStaleRead           Code = "stale_read"
	CodeNoMatch             Code = "no_match"
	CodeAmbiguousMatch      Code = "ambiguous_match"
	CodeNetwork             Code = "network"
	CodeBlockedHost         Code = "blocked_host"
	CodeModuleUnavailable   Code = "module_unavailable"
	CodeAuthRequired        Code = "auth_required"
	CodeOfflineMode         Code = "offline_mode"
	CodeDiskFull            Code = "disk_full"
	CodeBudgetExhausted     Code = "budget_exhausted"
	CodeColdOutboundDenied  Code = "cold_outbound_denied"
	CodeDBBusy              Code = "db_busy"
	CodeBlobMissing         Code = "blob_missing"
	CodeBlobExpired         Code = "blob_expired"
	CodeHasForks            Code = "has_forks"
	CodeSessionPurging      Code = "session_purging"
	CodeSessionBusy         Code = "session_busy"
	CodeLeaseFenced         Code = "lease_fenced"
	CodePurgeInProgress     Code = "purge_in_progress"
	CodeSchemaNewer         Code = "schema_newer"
	CodeApprovalNotPending  Code = "approval_not_pending"
	CodeIdempotencyConflict Code = "idempotency_conflict"
	CodeWriteVerifyFailed   Code = "write_verify_failed"
	CodeEgressDisabled      Code = "egress_disabled"
	CodeInvalidSkill        Code = "invalid_skill"
	CodeInternal            Code = "internal"
)

// CodeSet returns every closed error code (SPEC §4.3).
func CodeSet() map[Code]bool {
	all := []Code{
		CodeUnknownTool, CodeNotLoaded, CodeInvalidArgs, CodeNotFound,
		CodeOutsideWorkspace, CodeDeniedPath, CodeDenied, CodeHookDenied,
		CodeTimeout, CodeTooLarge, CodeExecFailed, CodeStaleRead, CodeNoMatch,
		CodeAmbiguousMatch, CodeNetwork, CodeBlockedHost, CodeModuleUnavailable,
		CodeAuthRequired, CodeOfflineMode, CodeDiskFull, CodeBudgetExhausted,
		CodeColdOutboundDenied, CodeDBBusy, CodeBlobMissing, CodeBlobExpired,
		CodeHasForks, CodeSessionPurging, CodeSessionBusy, CodeLeaseFenced,
		CodePurgeInProgress, CodeSchemaNewer, CodeApprovalNotPending,
		CodeIdempotencyConflict, CodeWriteVerifyFailed, CodeEgressDisabled,
		CodeInvalidSkill, CodeInternal,
	}
	set := make(map[Code]bool, len(all))
	for _, c := range all {
		set[c] = true
	}
	return set
}

// ToolError is the structured failure returned to the model (SPEC §4.3).
// Message addresses the model; Err never crosses into the model.
type ToolError struct {
	Code    Code
	Message string
	Hint    string
	Err     error
}

// Error implements error.
func (e *ToolError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("tools: %s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("tools: %s: %s", e.Code, e.Message)
}

// Unwrap exposes the cause to errors.Is/As.
func (e *ToolError) Unwrap() error { return e.Err }

func toolErr(c Code, hint, format string, args ...any) *ToolError {
	return &ToolError{Code: c, Message: fmt.Sprintf(format, args...), Hint: hint}
}

// codeOf maps err to a closed code: "" for nil, CodeInternal otherwise.
// Classification is by type, never by message text (E-21).
func codeOf(err error) Code {
	if err == nil {
		return ""
	}
	if te, ok := errors.AsType[*ToolError](err); ok {
		return te.Code
	}
	return CodeInternal
}

// SchemaDoc is the canonical model-facing tool document. The parameters
// object is compiled with internal/schema at registration.
type SchemaDoc struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Entry is one registered tool with its compiled parameter schema.
type Entry struct {
	Tool     Tool
	Doc      SchemaDoc
	Raw      json.RawMessage
	compiled *schema.Schema
}

// Validate checks args against the entry's compiled schema. A null value at
// the top level is treated as an absent optional argument (EC-TCALL-008),
// so tools apply their schema defaults; a missing required property still
// fails.
func (e *Entry) Validate(args json.RawMessage) error {
	args = dropNulls(args)
	if err := e.compiled.Validate(args); err != nil {
		return invalidArgs(err)
	}
	return nil
}

// dropNulls removes top-level null members from a JSON object, leaving
// every other byte untouched (the common fast path allocates nothing).
func dropNulls(args json.RawMessage) json.RawMessage {
	if !bytes.Contains(args, []byte("null")) {
		return args
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(args, &m) != nil {
		return args // let the validator report the syntax error
	}
	for k, v := range m {
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			delete(m, k)
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return args
	}
	return b
}

// invalidArgs renders a schema failure as invalid_args with the JSON
// Pointer, keyword and expectation (SPEC §4.3 step 3, ≤ 5 violations).
func invalidArgs(err error) *ToolError {
	if se, ok := errors.AsType[*schema.Error](err); ok {
		if se.Kind == schema.KindViolation {
			return &ToolError{
				Code:    CodeInvalidArgs,
				Message: fmt.Sprintf("%s: %s", orRoot(se.Pointer), se.Msg),
				Hint:    "fix the argument named by the JSON Pointer, then retry",
				Err:     err,
			}
		}
		return &ToolError{
			Code:    CodeInvalidArgs,
			Message: fmt.Sprintf("arguments: invalid JSON at byte %d: %s", se.Offset, se.Msg),
			Hint:    "send one JSON object matching the tool schema",
			Err:     err,
		}
	}
	return &ToolError{Code: CodeInvalidArgs, Message: "arguments: " + err.Error(), Err: err}
}

func orRoot(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

// Registry is an immutable, name-indexed tool set. Schemas returns the
// documents sorted byte-wise by name (SPEC §4.5 tool tier determinism).
type Registry struct {
	entries []*Entry
	byName  map[string]*Entry
	schemas []json.RawMessage
}

// NewRegistry compiles every tool's parameter schema and rejects duplicate
// or invalid registrations.
func NewRegistry(list ...Tool) (*Registry, error) {
	r := &Registry{byName: make(map[string]*Entry, len(list))}
	for _, t := range list {
		if t == nil {
			return nil, errors.New("tools: nil tool")
		}
		name := t.Name()
		if name == "" {
			return nil, errors.New("tools: tool with empty name")
		}
		if _, dup := r.byName[name]; dup {
			return nil, fmt.Errorf("tools: duplicate tool %q", name)
		}
		raw := t.Schema()
		var doc SchemaDoc
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("tools: %s schema: %w", name, err)
		}
		if doc.Name != name {
			return nil, fmt.Errorf("tools: %s schema name %q", name, doc.Name)
		}
		if len(doc.Description) > 160 {
			return nil, fmt.Errorf("tools: %s description longer than 160 chars", name)
		}
		if len(doc.Parameters) == 0 {
			return nil, fmt.Errorf("tools: %s schema has no parameters", name)
		}
		compiled, err := schema.Compile(doc.Parameters, schema.ModeInternal)
		if err != nil {
			return nil, fmt.Errorf("tools: %s parameters: %w", name, err)
		}
		r.byName[name] = &Entry{Tool: t, Doc: doc, Raw: raw, compiled: compiled}
	}
	r.entries = make([]*Entry, 0, len(r.byName))
	for _, e := range r.byName {
		r.entries = append(r.entries, e)
	}
	sort.Slice(r.entries, func(i, j int) bool { return r.entries[i].Doc.Name < r.entries[j].Doc.Name })
	for _, e := range r.entries {
		r.schemas = append(r.schemas, e.Raw)
	}
	return r, nil
}

// Lookup resolves a tool name (step 1 of the pipeline).
func (r *Registry) Lookup(name string) (*Entry, bool) {
	e, ok := r.byName[name]
	return e, ok
}

// Names returns the registered names in schema order.
func (r *Registry) Names() []string {
	out := make([]string, len(r.entries))
	for i, e := range r.entries {
		out[i] = e.Doc.Name
	}
	return out
}

// Schemas returns the sorted model-facing documents (assemble tier 4).
func (r *Registry) Schemas() []json.RawMessage {
	out := make([]json.RawMessage, len(r.schemas))
	copy(out, r.schemas)
	return out
}

// argsHash is the journal idempotency key of one call (SPEC §4.3 step 8).
// It never allocates.
func argsHash(args json.RawMessage) [32]byte { return sha256.Sum256(args) }

// Spill stores oversized model-facing output outside the transcript
// (SPEC P5, §11.6). Implementations are content-addressed and safe to call
// concurrently; FileSpill (F1) writes under <data_dir>/blobs.
type Spill interface {
	// Put stores data and returns its content-addressed reference.
	Put(ctx context.Context, data []byte) (BlobRef, error)
	// Get reads a blob back (spill round-trip, preview replay).
	Get(ctx context.Context, sha string) ([]byte, error)
}
