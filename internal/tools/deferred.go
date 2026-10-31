package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/damnbot/damnbot/internal/boundary"
	"github.com/damnbot/damnbot/internal/modules"
	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/schema"
)

// Deferred tool loading (SPEC §5.2, §14.1): the model sees eight eager tools
// plus the two meta-tools below. A deferred tool's schema is loaded through
// tool_search and the tool runs through tool_invoke, which reuses the whole
// dispatch pipeline (schema, effect, sandbox, taint, approval, journal,
// redaction, spill). Nothing about approval lives in this file.
const (
	// ToolSearchName is the catalog search meta-tool.
	ToolSearchName = "tool_search"
	// ToolInvokeName is the deferred invocation meta-tool.
	ToolInvokeName = "tool_invoke"
	// MaxSearchQuery is the longest accepted query (SPEC §5.2).
	MaxSearchQuery = 200
	// MaxSearchResults is the hard result bound of tool_search (k 1–5).
	MaxSearchResults = 5
	maxSearchTerms   = 8
)

// DeferredTool is one searchable tool contributed by a module. It is the
// tools-side view of modules.ToolDescriptor; the composition root maps the
// descriptor into it so internal/tools stays the owner of matching/rendering.
type DeferredTool struct {
	Name        string
	Description string
	Group       string
	Module      string
	Effect      string
	Tags        []string
	Schema      json.RawMessage

	compiled *schema.Schema
}

// Catalog is an immutable, name-indexed set of deferred tools.
type Catalog struct {
	tools  []DeferredTool // sorted by Name
	byName map[string]int
}

// NewCatalog validates and indexes list. Duplicate names and schemas outside
// the internal subset are construction errors, so a broken module fails at
// wiring time rather than at the first model call.
func NewCatalog(list []DeferredTool) (*Catalog, error) {
	sorted := make([]DeferredTool, len(list))
	copy(sorted, list)
	slices.SortFunc(sorted, func(a, b DeferredTool) int { return strings.Compare(a.Name, b.Name) })
	c := &Catalog{tools: sorted, byName: make(map[string]int, len(sorted))}
	for i := range sorted {
		t := &sorted[i]
		switch {
		case t.Name == "":
			return nil, errors.New("tools: deferred tool with empty name")
		case strings.TrimSpace(t.Description) == "":
			return nil, fmt.Errorf("tools: deferred tool %q has no description", t.Name)
		case len(t.Schema) == 0:
			return nil, fmt.Errorf("tools: deferred tool %q has no schema", t.Name)
		}
		compiled, err := schema.Compile(t.Schema, schema.ModeInternal)
		if err != nil {
			return nil, fmt.Errorf("tools: deferred tool %q: %w", t.Name, err)
		}
		if _, dup := c.byName[t.Name]; dup {
			return nil, fmt.Errorf("tools: duplicate deferred tool %q", t.Name)
		}
		t.compiled = compiled
		c.byName[t.Name] = i
	}
	return c, nil
}

// Len returns the number of deferred tools.
func (c *Catalog) Len() int { return len(c.tools) }

// Lookup resolves one deferred tool by exact name.
func (c *Catalog) Lookup(name string) (DeferredTool, bool) {
	i, ok := c.byName[name]
	if !ok {
		return DeferredTool{}, false
	}
	return c.tools[i], true
}

// Names returns every deferred tool name in catalog order (for tests and the
// `damnbot config modules` surface).
func (c *Catalog) Names() []string {
	out := make([]string, len(c.tools))
	for i := range c.tools {
		out[i] = c.tools[i].Name
	}
	return out
}

// Search returns at most k matches for query, optionally restricted to one
// group. Matching is case-insensitive over name, description and tags; all
// query terms must match. k is clamped to [1, MaxSearchResults] so a caller
// cannot ask for an unbounded result, and the order is deterministic: an
// exact-name or name-substring match first, then name order.
func (c *Catalog) Search(query, group string, k int) []DeferredTool {
	k = min(max(k, 1), MaxSearchResults)
	if len(query) > MaxSearchQuery {
		return nil
	}
	var terms []string
	for term := range strings.FieldsSeq(strings.ToLower(query)) {
		if term == "" {
			continue
		}
		if len(terms) == maxSearchTerms {
			break
		}
		terms = append(terms, term)
	}
	if len(terms) == 0 {
		return nil
	}
	lowerQuery := strings.ToLower(strings.TrimSpace(query))
	type scored struct {
		tool  DeferredTool
		score int
	}
	var hits []scored
	for i := range c.tools {
		t := &c.tools[i]
		if group != "" && t.Group != group {
			continue
		}
		if !matchesAll(t, terms) {
			continue
		}
		score := 1
		if strings.Contains(strings.ToLower(t.Name), lowerQuery) {
			score = 2
		}
		hits = append(hits, scored{tool: *t, score: score})
	}
	slices.SortStableFunc(hits, func(a, b scored) int { return b.score - a.score })
	out := make([]DeferredTool, 0, min(k, len(hits)))
	for i := range hits[:min(k, len(hits))] {
		out = append(out, hits[i].tool)
	}
	return out
}

// matchesAll reports whether every query term occurs in the searchable text.
func matchesAll(t *DeferredTool, terms []string) bool {
	var b strings.Builder
	b.WriteString(strings.ToLower(t.Name))
	b.WriteByte('\n')
	b.WriteString(strings.ToLower(t.Description))
	for _, tag := range t.Tags {
		b.WriteByte('\n')
		b.WriteString(strings.ToLower(tag))
	}
	haystack := b.String()
	for _, term := range terms {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}

// entry builds a dispatcher entry for dt backed by mod. The schema was
// compiled by NewCatalog, so this cannot fail.
func (c *Catalog) entry(dt *DeferredTool, mod modules.Module, rt ModuleRuntime) *Entry {
	tool := &moduleTool{name: dt.Name, module: dt.Module, schema: dt.Schema, effect: dt.Effect, mod: mod, runtime: rt}
	return &Entry{
		Tool:     tool,
		Doc:      SchemaDoc{Name: dt.Name, Description: dt.Description, Parameters: dt.Schema},
		compiled: dt.compiled,
	}
}

// Deferred configures tool_search and tool_invoke for one dispatcher.
// Catalog supplies the searchable metadata; Runtime activates the owning
// module on the first invoke. Both are optional: without a catalog the two
// meta-tools report module_unavailable instead of guessing.
type Deferred struct {
	Catalog *Catalog
	Runtime ModuleRuntime
}

// ModuleRuntime is the lifecycle side of deferred modules (E-09). It is
// satisfied by modules.Runtime: activation must be idempotent, and Ensure on
// a needs_attention module must fail rather than retry silently.
type ModuleRuntime interface {
	Ensure(ctx context.Context, id string) error
	Module(id string) (modules.Module, bool)
	Fail(ctx context.Context, id string, cause error) error
}

// moduleTool adapts one live module to the Tool interface. It never decides
// policy: the dispatcher does, from Effect and the module result.
type moduleTool struct {
	name    string
	module  string
	schema  json.RawMessage
	effect  string
	mod     modules.Module
	runtime ModuleRuntime
}

func (t *moduleTool) Name() string            { return t.name }
func (t *moduleTool) Schema() json.RawMessage { return t.schema }
func (t *moduleTool) Kind() Kind {
	if t.Effect(json.RawMessage(nil)) == EffectRead {
		return KindRead
	}
	return KindOther
}

// Effect maps the module's declared class onto the pipeline. An unknown
// class fails closed as dynamic, which asks before every run.
func (t *moduleTool) Effect(json.RawMessage) Effect {
	switch t.effect {
	case "read":
		return EffectRead
	case "idempotent":
		return EffectIdempotent
	case "non_idempotent":
		return EffectNonIdempotent
	}
	return effectDynamic
}

// Run calls the module inside a boundary guard: a module panic becomes
// `internal`, the module is marked failed, and the process survives (P14).
func (t *moduleTool) Run(ctx context.Context, env Env, args json.RawMessage) (Result, error) {
	var (
		res host.Result
		err error
	)
	perr := boundary.Run(ctx, "tools.module."+t.module, func(ctx context.Context) error {
		res, err = t.mod.Invoke(ctx, t.name, args, host.Env{
			TurnID: env.TurnID, CallID: env.CallID, Cwd: env.Cwd, Approved: env.Approved,
			Session: env.Session,
		})
		return nil
	})
	if perr != nil {
		if t.runtime != nil {
			if ferr := t.runtime.Fail(ctx, t.module, perr); ferr != nil {
				return Result{}, &ToolError{Code: CodeInternal, Message: "module panicked", Hint: "module marked failed", Err: errors.Join(perr, ferr)}
			}
		}
		return Result{}, &ToolError{Code: CodeInternal, Message: "module panicked", Hint: "module marked failed", Err: perr}
	}
	if err != nil {
		return Result{}, moduleToolError(err)
	}
	if verr := res.Validate(); verr != nil {
		return Result{}, &ToolError{Code: CodeInternal, Message: "module returned an invalid result", Err: verr}
	}
	return Result{OK: true, Content: res.Content, Meta: res.Meta, UntrustedSource: res.UntrustedSource}, nil
}

// moduleToolError maps a module failure onto the closed tool-code set. A
// host.Error code outside the set becomes internal (fail-closed).
func moduleToolError(err error) error {
	var he *host.Error
	if errors.As(err, &he) && CodeSet()[Code(he.Code)] {
		return &ToolError{Code: Code(he.Code), Message: he.Message, Hint: he.Hint, Err: err}
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return &ToolError{Code: CodeTimeout, Message: "module call timed out", Err: err}
	default:
		return &ToolError{Code: CodeInternal, Message: "module call failed", Err: err}
	}
}

// schemaToolSearch is the deferred catalog search meta-tool (SPEC §5.2).
const schemaToolSearch = `{"name":"tool_search","description":"Search deferred tools by name, description or tag; returns up to k full tool schemas.","parameters":{"type":"object","properties":{"query":{"type":"string","minLength":1,"maxLength":200},"group":{"type":"string","minLength":1,"maxLength":64},"k":{"type":"integer","minimum":1,"maximum":5}},"required":["query"],"additionalProperties":false}}`

// schemaToolInvoke is the deferred invocation meta-tool (SPEC §5.2).
const schemaToolInvoke = `{"name":"tool_invoke","description":"Invoke a tool by its exact catalog name with the same approval and sandbox rules as a direct call.","parameters":{"type":"object","properties":{"name":{"type":"string","minLength":1,"maxLength":64},"arguments":{"type":"object"}},"required":["name"],"additionalProperties":false}}`

// toolSearch implements tool_search over a Catalog.
type toolSearch struct {
	catalog *Catalog
	status  func(moduleID string) string
}

// NewToolSearch returns the tool_search tool. status, when non-nil, reports
// the owning module's lifecycle status next to each hit; it must not activate
// anything.
func NewToolSearch(catalog *Catalog, status func(moduleID string) string) Tool {
	return &toolSearch{catalog: catalog, status: status}
}

func (t *toolSearch) Name() string            { return ToolSearchName }
func (t *toolSearch) Schema() json.RawMessage { return json.RawMessage(schemaToolSearch) }
func (t *toolSearch) Kind() Kind              { return KindSearch }
func (t *toolSearch) Effect(json.RawMessage) Effect {
	return EffectRead
}

// deferredHit is one search result (SPEC §5.2: name, description, full
// schema, module status; at most five).
type deferredHit struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Module      string          `json:"module"`
	Status      string          `json:"status"`
	Schema      json.RawMessage `json:"schema"`
}

// searchArgs is the validated tool_search argument object.
type searchArgs struct {
	Query string `json:"query"`
	Group string `json:"group"`
	K     int    `json:"k"`
}

// Run renders the bounded catalog result. A missing catalog is a wiring
// error and fails closed instead of searching an empty set.
func (t *toolSearch) Run(_ context.Context, _ Env, args json.RawMessage) (Result, error) {
	if t.catalog == nil {
		return Result{}, &ToolError{Code: CodeModuleUnavailable, Message: "tool catalog is not configured", Hint: "register the module catalog before using tool_search"}
	}
	var in searchArgs
	if err := json.Unmarshal(args, &in); err != nil {
		return Result{}, &ToolError{Code: CodeInvalidArgs, Message: "arguments: " + err.Error()}
	}
	if in.K == 0 {
		in.K = MaxSearchResults
	}
	hits := t.catalog.Search(in.Query, in.Group, in.K)
	out := struct {
		Tools []deferredHit `json:"tools"`
	}{Tools: make([]deferredHit, 0, len(hits))}
	for i := range hits {
		h := &hits[i]
		out.Tools = append(out.Tools, deferredHit{
			Name: h.Name, Description: h.Description, Module: h.Module,
			Status: t.moduleStatus(h.Module), Schema: h.Schema,
		})
	}
	body, err := json.Marshal(out)
	if err != nil {
		return Result{}, &ToolError{Code: CodeInternal, Message: "result encoding failed", Err: err}
	}
	return Result{OK: true, Content: string(body), Meta: map[string]any{"count": len(out.Tools), "k": in.K}}, nil
}

func (t *toolSearch) moduleStatus(id string) string {
	if t.status == nil {
		return "unknown"
	}
	return t.status(id)
}

// invokeTool is the registered tool_invoke marker. Dispatcher.prepare
// unwraps it before classification; Run only exists so the registry can
// carry the schema, and fails closed if a dispatcher was wired without
// deferred support.
type invokeTool struct{}

// NewToolInvoke returns the tool_invoke tool.
func NewToolInvoke() Tool { return invokeTool{} }

func (invokeTool) Name() string            { return ToolInvokeName }
func (invokeTool) Schema() json.RawMessage { return json.RawMessage(schemaToolInvoke) }
func (invokeTool) Kind() Kind              { return KindOther }
func (invokeTool) Effect(json.RawMessage) Effect {
	return effectDynamic
}

func (invokeTool) Run(context.Context, Env, json.RawMessage) (Result, error) {
	return Result{}, &ToolError{
		Code:    CodeModuleUnavailable,
		Message: "tool_invoke requires a dispatcher with a deferred catalog",
		Hint:    "wire tools.Deferred into the dispatcher",
	}
}

// invokeArgs is the validated tool_invoke envelope.
type invokeArgs struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// unwrapInvoke resolves tool_invoke into the call it forwards, activating
// the owning module first. It runs inside Prepare, before lookup/schema/
// sandbox/taint/policy, so the forwarded call is authorised exactly like a
// direct call and no nested dispatch is created.
//
//nolint:gocritic // Call is a value carrier with a fixed public shape (ADR-203 added PlanMode)
func (d *Dispatcher) unwrapInvoke(call Call) (Call, *Outcome) {
	entry, ok := d.reg.Lookup(ToolInvokeName)
	if !ok {
		return call, nil // not registered: normal unknown_tool path
	}
	if d.deferred == nil || d.deferred.Catalog == nil {
		out := failure(CodeModuleUnavailable, "deferred tool catalog is not configured", "wire the module catalog into the dispatcher")
		return call, &out
	}
	args := call.Args
	if len(strings.TrimSpace(string(args))) == 0 {
		args = json.RawMessage(`{}`)
	}
	if err := entry.Validate(args); err != nil {
		out := failure(codeOf(err), messageOf(err), hintOf(err))
		return call, &out
	}
	var env invokeArgs
	if err := json.Unmarshal(args, &env); err != nil {
		out := failure(CodeInvalidArgs, "arguments: "+err.Error(), "send {\"name\":…,\"arguments\":{…}}")
		return call, &out
	}
	if env.Name == ToolInvokeName {
		out := failure(CodeInvalidArgs, "tool_invoke cannot invoke itself", "name the target tool")
		return call, &out
	}
	inner := call
	inner.Name = env.Name
	if len(strings.TrimSpace(string(env.Arguments))) == 0 {
		inner.Args = json.RawMessage(`{}`)
	} else {
		inner.Args = env.Arguments
	}
	return inner, nil
}

// planDeferredDeny refuses a plan-mode call to a deferred non-read tool before
// the runtime is asked to activate the owning module (F-1, ADR-203). It
// returns handled=false for eager tools, unknown names, and read-effect
// deferred tools, which continue through classifyCall.
func (d *Dispatcher) planDeferredDeny(call *Call) (Prepared, bool, error) {
	if d.deferred == nil || d.deferred.Catalog == nil {
		return Prepared{}, false, nil
	}
	if _, ok := d.reg.Lookup(call.Name); ok {
		return Prepared{}, false, nil
	}
	dt, ok := d.deferred.Catalog.Lookup(call.Name)
	if !ok {
		return Prepared{}, false, nil
	}
	mt := moduleTool{effect: dt.Effect}
	effect := mt.Effect(nil)
	if effect == EffectRead {
		return Prepared{}, false, nil
	}
	args := call.Args
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage(`{}`)
	}
	entry := &Entry{
		Doc:      SchemaDoc{Name: dt.Name, Description: dt.Description, Parameters: dt.Schema},
		compiled: dt.compiled,
	}
	if err := entry.Validate(args); err != nil {
		out := failure(codeOf(err), messageOf(err), hintOf(err))
		return Prepared{}, true, out.Err
	}
	decision := PolicyDecision{Requirement: RequireDeny, Class: "plan_mode"}
	ensurePreview(call.Name, args, effect, &decision)
	return Prepared{
		Call: *call, Args: args, Effect: effect, Decision: decision,
		Env:   Env{TurnID: call.TurnID, CallID: call.CallID, Cwd: d.cwd, PageChars: int32Cap(d.pageCap()), InlineLeft: int32Cap(d.inlineLeft())},
		entry: entry,
	}, true, nil
}

// deferredEntry resolves a name that is not in the eager registry, activating
// the owning module. Activation happens on tool_invoke only: tool_search
// never starts a module.
func (d *Dispatcher) deferredEntry(ctx context.Context, name string) (*Entry, *Outcome) {
	if d.deferred == nil || d.deferred.Catalog == nil {
		out := failure(CodeModuleUnavailable, "deferred tool catalog is not configured", "")
		return nil, &out
	}
	dt, ok := d.deferred.Catalog.Lookup(name)
	if !ok {
		out := failure(CodeUnknownTool, "unknown tool "+quote(name), "use a name returned by tool_search")
		return nil, &out
	}
	rt := d.deferred.Runtime
	if rt == nil {
		out := failure(CodeModuleUnavailable, "module runtime is not configured", "wire modules.Runtime into the dispatcher")
		return nil, &out
	}
	if err := rt.Ensure(ctx, dt.Module); err != nil {
		out := activationOutcome(&dt, err)
		return nil, &out
	}
	mod, ok := rt.Module(dt.Module)
	if !ok {
		out := failure(CodeModuleUnavailable, "module "+dt.Module+" is not active", "retry after the module recovers")
		return nil, &out
	}
	return d.deferred.Catalog.entry(&dt, mod, rt), nil
}

// activationOutcome maps modules lifecycle errors onto the closed code set.
func activationOutcome(dt *DeferredTool, err error) Outcome {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return failure(CodeTimeout, "module activation cancelled for "+dt.Module, "")
	case errors.Is(err, modules.ErrNeedsAttention):
		return failure(CodeModuleUnavailable, "module "+dt.Module+" needs owner attention", "resolve the module (login/pairing/restart), then retry")
	case errors.Is(err, modules.ErrDisabled):
		return failure(CodeModuleUnavailable, "module "+dt.Module+" is disabled", "enable it in modules."+dt.Module)
	default:
		return failure(CodeModuleUnavailable, "module "+dt.Module+" is unavailable", "retry later or check `damnbot config modules`")
	}
}
