package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/boundary"
	"github.com/damnbot/damnbot/internal/codetools"
	"github.com/damnbot/damnbot/internal/redact"
)

// Inline caps of SPEC P5 / ADR-031.
const (
	InlineChars     = 4000  // one non-paged result
	TurnInlineChars = 24000 // aggregate inline characters per turn
	PageChars       = 16000 // paged tool output before the aggregate is spent
	ReducedPage     = 4000  // page cap after it is spent
	SpillPreview    = 800   // preview characters when a result spills
	ShortPreview    = 400   // preview after the aggregate is spent
)

// Journal is the consumer-side transaction interface of SPEC §4.3 steps 8
// and 10 (ENGINEERING E-09). The composition root provides the event-store
// adapter (loop/event) when the journal lands in F2; F1 supplies a recorder.
// Both methods are called inside the dispatch critical section: a dispatch
// record precedes Run, a result record follows it.
type Journal interface {
	ToolDispatch(ctx context.Context, turnID, callID, tool string, effect Effect, argsHash [32]byte) error
	ToolResult(ctx context.Context, turnID, callID, tool string, ok bool, code, content string) error
}

// Config wires one session's dispatcher.
type Config struct {
	Registry *Registry
	Journal  Journal // required
	Spill    Spill   // required for oversized results
	Mode     Mode
	Approver Approver // nil: headless behaviour even in interactive modes
	Redactor *redact.Redactor
	// ToolTimeout bounds Run; 0 leaves the caller's context as the only
	// deadline (shell enforces its own timeout_s).
	ToolTimeout      time.Duration
	WorkspaceTrusted bool
	Cwd              string
	// PageChars overrides the paged-tool cap (tests); 0 = PageChars.
	PageChars int
}

// Call is one tool invocation. CallID and TurnID key the journal and the
// per-turn duplicate guard; empty CallID disables the guard.
type Call struct {
	TurnID string
	CallID string
	Name   string
	Args   json.RawMessage
}

// Outcome is the rendered result of one Dispatch. Render produces the exact
// model-facing string: content on success, the SPEC §4.3 error envelope on
// failure.
type Outcome struct {
	Content     string
	Meta        map[string]any
	Attachments []BlobRef
	Err         *ToolError
	Denied      bool // approval denied: the loop stops with approval_denied
	Cancelled   bool // the call's context ended
	Tainted     bool // the result tainted the turn (ADR-070)
	Spilled     *BlobRef
}

// Render returns the model-facing text (SPEC §4.3 envelope).
func (o Outcome) Render() string {
	if o.Err == nil {
		return o.Content
	}
	body := struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    Code   `json:"code"`
			Message string `json:"message"`
			Hint    string `json:"hint,omitempty"`
		} `json:"error"`
	}{}
	body.OK = false
	body.Error.Code = o.Err.Code
	body.Error.Message = o.Err.Message
	body.Error.Hint = o.Err.Hint
	b, err := json.Marshal(body)
	if err != nil {
		return `{"ok":false,"error":{"code":"internal","message":"result encoding failed"}}`
	}
	return string(b)
}

// Dispatcher runs the SPEC §4.3 pipeline for one session. Reads may run in
// parallel (SPEC §4.2); mutating calls are serialised by the loop, not here.
// Turn-scoped state (taint, inline aggregate, seen call ids) is reset by
// BeginTurn.
type Dispatcher struct {
	reg      *Registry
	journal  Journal
	spill    Spill
	mode     Mode
	approver Approver
	red      *redact.Redactor
	timeout  time.Duration
	trusted  bool
	cwd      string
	page     int

	tainted  atomic.Bool
	mu       sync.Mutex
	inline   int
	calls    map[string]bool
	sessions map[string]bool
}

// NewDispatcher validates the required wiring. Journal, Spill and Redactor
// are required: a dispatcher without them would silently lose the transaction
// boundary, overflow the model context, or emit unredacted output.
func NewDispatcher(cfg *Config) (*Dispatcher, error) {
	switch {
	case cfg.Registry == nil:
		return nil, errors.New("tools: registry required")
	case cfg.Journal == nil:
		return nil, errors.New("tools: journal required")
	case cfg.Spill == nil:
		return nil, errors.New("tools: spill required")
	case cfg.Redactor == nil:
		// Fail closed: a dispatcher without redaction would leak secrets
		// from every tool result to the model (ADR-071, REVIEW-F1-SEC7
		// TL7-3).
		return nil, errors.New("tools: redactor required")
	}
	return &Dispatcher{
		reg: cfg.Registry, journal: cfg.Journal, spill: cfg.Spill, mode: cfg.Mode,
		approver: cfg.Approver, red: cfg.Redactor, timeout: cfg.ToolTimeout,
		trusted: cfg.WorkspaceTrusted, cwd: cfg.Cwd, page: cfg.PageChars,
		calls: map[string]bool{}, sessions: map[string]bool{},
	}, nil
}

// BeginTurn resets turn-scoped state: taint, inline aggregate and the
// duplicate-call guard. allow_session decisions belong to the session and
// survive.
func (d *Dispatcher) BeginTurn() {
	d.tainted.Store(false)
	d.mu.Lock()
	d.inline = 0
	clear(d.calls)
	d.mu.Unlock()
}

// Tainted reports the sticky turn taint (ADR-070).
func (d *Dispatcher) Tainted() bool { return d.tainted.Load() }

// MarkTainted taints the rest of the turn (a caller-observed untrusted
// result outside Dispatch, e.g. a provider-side wrapper).
func (d *Dispatcher) MarkTainted() { d.tainted.Store(true) }

// Prepared is one call after pipeline steps 1–6: decoded, validated,
// sandboxed and classified. Prepare never approves, journals or runs; the
// loop performs approval and then calls DispatchPrepared, so policy and
// approval run exactly once (loop.Toolset shape).
type Prepared struct {
	Call     Call
	Args     json.RawMessage
	Effect   Effect
	Decision PolicyDecision
	Env      Env
	// Approved is set by the caller when an outside_root/hardlinked verdict
	// was approved; DispatchPrepared passes it to the tool.
	Approved bool
	// authorized records that the dispatcher itself approved this prepared
	// call; done records that it has been executed. Both keep policy and
	// execution single-shot (REVIEW-F1-SEC7 TL7-11).
	authorized bool
	done       bool
	entry      *Entry
}

// Prepare runs steps 1–6 (lookup, strict JSON/schema, effect, sandbox,
// taint gate, Inspector policy) and returns the classification. It never
// consults the approver, writes the journal or runs the tool.
func (d *Dispatcher) Prepare(ctx context.Context, call Call) (Prepared, error) {
	var p Prepared
	err := boundary.Run(ctx, "tools.prepare", func(ctx context.Context) error {
		got, perr := d.prepare(ctx, call)
		p = got
		return perr
	})
	if err != nil {
		return Prepared{}, closedError(err)
	}
	return p, nil
}

func (d *Dispatcher) prepare(ctx context.Context, call Call) (Prepared, error) {
	if err := ctx.Err(); err != nil {
		return Prepared{}, &ToolError{Code: CodeTimeout, Message: "call cancelled", Err: err}
	}
	entry, args, effect, fail := d.classifyCall(call)
	if fail != nil {
		return Prepared{}, fail.Err
	}
	pc := PolicyContext{Mode: d.mode, Tainted: d.tainted.Load(), WorkspaceTrusted: d.trusted, Cwd: d.cwd}
	decision, perr := d.policy(ctx, entry, Env{TurnID: call.TurnID, CallID: call.CallID, Cwd: d.cwd}, args, pc, effect)
	if perr != nil {
		return Prepared{}, closedError(perr)
	}
	decision = d.taintGate(decision, effect, pc)
	return Prepared{
		Call: call, Args: args, Effect: effect, Decision: decision,
		Env:   Env{TurnID: call.TurnID, CallID: call.CallID, Cwd: d.cwd, PageChars: d.pageCap(), InlineLeft: d.inlineLeft()},
		entry: entry,
	}, nil
}

// DispatchPrepared runs steps 7–10 for a prepared call: journal dispatch,
// Run, redaction/paging/spill and journal result. It never re-runs policy.
func (d *Dispatcher) DispatchPrepared(ctx context.Context, p *Prepared) Outcome {
	var out Outcome
	err := boundary.Run(ctx, "tools.dispatch", func(ctx context.Context) error {
		out = d.dispatchPrepared(ctx, p)
		return nil
	})
	if err != nil {
		return fromError(err)
	}
	return out
}

func (d *Dispatcher) dispatchPrepared(ctx context.Context, p *Prepared) Outcome {
	if err := ctx.Err(); err != nil {
		return failure(CodeTimeout, "call cancelled", "")
	}
	if p == nil || p.entry == nil {
		return failure(CodeInternal, "dispatch of an unprepared call", "call Prepare first")
	}
	if p.done {
		return failure(CodeInternal, "prepared call already dispatched", "call Prepare again")
	}
	// Approval runs exactly once: Dispatch authorizes before calling here,
	// the loop may authorize itself and set Approved, and a bare caller gets
	// authorized here. Hard denials are re-derived and never executable
	// (REVIEW-F1-SEC7 TL7-11, ADR-079).
	switch p.Decision.Requirement {
	case RequireDeny, RequireDeniedPath:
		if fail := d.authorize(ctx, p); fail != nil {
			return *fail
		}
	case RequireAuto, RequireOutside, RequireAsk:
		if !p.authorized && !p.Approved {
			if fail := d.authorize(ctx, p); fail != nil {
				return *fail
			}
		}
	}
	p.done = true
	p.Env.Approved = p.Approved
	return d.execute(ctx, p)
}

// prepare runs steps 1–4: lookup, duplicate guard, schema validation and
// the effect class.
func (d *Dispatcher) classifyCall(call Call) (*Entry, json.RawMessage, Effect, *Outcome) {
	entry, ok := d.reg.Lookup(call.Name)
	if !ok {
		out := failure(CodeUnknownTool, "unknown tool "+quote(call.Name), "use a name from the tool list")
		return nil, nil, "", &out
	}
	args := call.Args
	if len(bytes.TrimSpace(args)) == 0 {
		args = json.RawMessage(`{}`)
	}
	if fail := d.claimCallID(call.CallID); fail != nil {
		return nil, nil, "", fail
	}
	if err := entry.Validate(args); err != nil {
		out := failure(codeOf(err), messageOf(err), hintOf(err))
		return nil, nil, "", &out
	}
	effect := entry.Tool.Effect(args)
	if !validEffect(effect) {
		out := failure(CodeInternal, "tool returned an invalid effect", "")
		return nil, nil, "", &out
	}
	return entry, args, effect, nil
}

// claimCallID rejects a second call with the same id in this turn
// (EC-TCALL-002); the tool never runs twice.
func (d *Dispatcher) claimCallID(id string) *Outcome {
	if id == "" {
		return nil
	}
	d.mu.Lock()
	dup := d.calls[id]
	if !dup {
		d.calls[id] = true
	}
	d.mu.Unlock()
	if !dup {
		return nil
	}
	out := failure(CodeIdempotencyConflict, "duplicate tool_call_id "+quote(id), "one result per call id; do not repeat the call")
	return &out
}

// execute runs steps 5–6: journal, Run, untrusted wrapping, spill and the
// result journal row.
func (d *Dispatcher) execute(ctx context.Context, p *Prepared) Outcome {
	call, entry, args, effect, decision := p.Call, p.entry, p.Args, p.Effect, p.Decision
	if err := d.journal.ToolDispatch(ctx, call.TurnID, call.CallID, call.Name, effect, argsHash(args)); err != nil {
		return failure(CodeInternal, "journal dispatch failed: "+err.Error(), "")
	}
	res, runErr := d.run(ctx, entry.Tool, p.Env, args)
	if runErr = normalizeRunErr(ctx, runErr); runErr != nil {
		out := fromError(runErr)
		if ctx.Err() != nil {
			out.Cancelled = true
		}
		d.record(ctx, call, &out)
		return out
	}
	// A tool may report failure without a Go error (Result.Err / OK=false);
	// that must never render as success (REVIEW-F1-SEC7 TL7-1).
	if res.Err != nil || !res.OK {
		out := Outcome{Content: res.Content, Meta: res.Meta, Attachments: res.Attachments, Err: res.Err}
		if out.Err == nil {
			out.Err = &ToolError{Code: CodeInternal, Message: "tool returned no result"}
		}
		if ctx.Err() != nil {
			out.Cancelled = true
		}
		d.record(ctx, call, &out)
		return out
	}
	// Reads that leave the workspace (or any read in an untrusted
	// workspace) are data, never instructions (SPEC §14.5, ADR-069/070).
	if effect == EffectRead && (entry.Tool.Kind() == KindRead || entry.Tool.Kind() == KindSearch) {
		switch {
		case decision.Requirement == RequireOutside:
			res.UntrustedSource = "file:outside_workspace"
		case !d.trusted:
			res.UntrustedSource = "workspace"
		}
	}
	out := d.finish(ctx, entry.Doc.Name, &res)
	d.record(ctx, call, &out)
	return out
}

// policy asks the tool's Inspector, or uses the default code-tool policy.
func (d *Dispatcher) policy(ctx context.Context, entry *Entry, env Env, args json.RawMessage, pc PolicyContext, effect Effect) (PolicyDecision, error) {
	if in, ok := entry.Tool.(Inspector); ok {
		dec, err := in.Inspect(ctx, env, args, pc)
		if err != nil {
			return PolicyDecision{}, fmt.Errorf("tools: inspect %s: %w", entry.Doc.Name, err)
		}
		return dec, nil
	}
	return defaultPolicy(effect, pc), nil
}

// taintGate applies ADR-070: while the turn is tainted, every non-read
// action asks in every mode (no allow_session) and headless denies.
func (d *Dispatcher) taintGate(dec PolicyDecision, effect Effect, pc PolicyContext) PolicyDecision {
	if !pc.Tainted || effect == EffectRead {
		return dec
	}
	dec.AllowSession = false
	if pc.Mode.Headless() {
		dec.Requirement = RequireDeny
		return dec
	}
	if dec.Requirement == RequireAuto {
		dec.Requirement = RequireAsk
	}
	return dec
}

// resolve turns a requirement into an execution decision. It reports
// denied=true when the approver refused.

// run executes the tool, optionally bounded by ToolTimeout.
func (d *Dispatcher) run(ctx context.Context, t Tool, env Env, args json.RawMessage) (Result, error) {
	var res Result
	var err error
	if d.timeout <= 0 {
		res, err = t.Run(ctx, env, args)
	} else {
		cctx, cancel := context.WithTimeout(ctx, d.timeout)
		defer cancel()
		res, err = t.Run(cctx, env, args)
	}
	if err != nil {
		return Result{}, fmt.Errorf("tools: run %s: %w", t.Name(), err)
	}
	return res, nil
}

// pageCap is the paged-tool cap for the next call (ADR-031).
func (d *Dispatcher) pageCap() int {
	base := d.page
	if base == 0 {
		base = PageChars
	}
	d.mu.Lock()
	spent := d.inline >= TurnInlineChars
	d.mu.Unlock()
	if spent {
		return ReducedPage
	}
	return base
}

// inlineLeft is what remains of the turn's inline budget.
func (d *Dispatcher) inlineLeft() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return max(0, TurnInlineChars-d.inline)
}

// finish applies redaction, untrusted wrapping, paging/spill and taint.
func (d *Dispatcher) finish(ctx context.Context, name string, res *Result) Outcome {
	out := Outcome{Meta: res.Meta, Attachments: res.Attachments}
	content := string(d.red.Redact([]byte(res.Content)))
	if res.UntrustedSource != "" {
		content = wrapUntrusted(content, res.UntrustedSource)
		out.Tainted = true
		d.tainted.Store(true)
	}
	if !paged(name) {
		content, out.Spilled, out.Meta = d.spillResult(ctx, content, out.Meta)
	}
	d.mu.Lock()
	d.inline += utf8.RuneCountInString(content)
	d.mu.Unlock()
	out.Content = content
	return out
}

// paged reports the ADR-031 tools: sourcing from disk, so they page instead
// of spilling.
func paged(name string) bool {
	switch name {
	case "read_file", "grep", "glob":
		return true
	}
	return false
}

// spillResult spills an oversized non-paged result and returns the preview.
func (d *Dispatcher) spillResult(ctx context.Context, content string, meta map[string]any) (string, *BlobRef, map[string]any) {
	if utf8.RuneCountInString(content) <= InlineChars {
		d.mu.Lock()
		fits := d.inline+utf8.RuneCountInString(content) <= TurnInlineChars
		d.mu.Unlock()
		if fits {
			return content, nil, meta
		}
	}
	ref, err := d.spill.Put(ctx, []byte(content))
	if err != nil {
		// Keep the head: the call already ran; disk_full stays visible.
		if meta == nil {
			meta = map[string]any{}
		}
		meta["spill_error"] = codeOf(err)
		return clipRunes(content, ShortPreview), nil, meta
	}
	d.mu.Lock()
	left := TurnInlineChars - d.inline
	d.mu.Unlock()
	n := SpillPreview
	if left < SpillPreview {
		n = ShortPreview
	}
	preview := clipRunes(content, n)
	preview += "\n[spill: " + ref.Path + " (" + itoa(len(content)) + " chars)]"
	if meta == nil {
		meta = map[string]any{}
	}
	meta["content_blob_sha"] = ref.SHA
	meta["chars"] = len(content)
	return preview, &ref, meta
}

// record appends the tool_result journal row. A failure here cannot undo a
// completed side effect; it is surfaced in Meta for the terminal turn.
func (d *Dispatcher) record(ctx context.Context, call Call, out *Outcome) {
	ok := out.Err == nil
	code := Code("")
	if out.Err != nil {
		code = out.Err.Code
	}
	if err := d.journal.ToolResult(ctx, call.TurnID, call.CallID, call.Name, ok, string(code), out.Content); err != nil {
		// The side effect already happened; the failed row is visible to
		// the terminal turn rather than being dropped silently.
		if out.Meta == nil {
			out.Meta = map[string]any{}
		}
		out.Meta["journal_error"] = true
	}
}

// normalizeRunErr maps a tool failure onto the closed code set. A context
// that ended maps to timeout, a typed ToolError keeps its code, anything
// else is internal.
func normalizeRunErr(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return &ToolError{Code: CodeTimeout, Message: "tool cancelled: " + ctx.Err().Error(), Err: err}
	}
	return closedError(err)
}

// closedError fails closed: a ToolError outside the closed set becomes
// internal, and any other error is wrapped as internal.
func closedError(err error) error {
	if ce, ok := errors.AsType[*codetools.ToolError](err); ok {
		if CodeSet()[Code(ce.Code)] {
			return &ToolError{Code: Code(ce.Code), Message: ce.Message, Hint: ce.Hint, Err: err}
		}
	}
	te, ok := errors.AsType[*ToolError](err)
	if !ok {
		return &ToolError{Code: CodeInternal, Message: err.Error(), Err: err}
	}
	if !CodeSet()[te.Code] {
		return &ToolError{Code: CodeInternal, Message: "tool returned a non-closed error code", Err: err}
	}
	return te
}

// fromError renders any error as an Outcome failure.
func fromError(err error) Outcome {
	err = closedError(err)
	te, _ := errors.AsType[*ToolError](err)
	return Outcome{Err: te}
}

func failure(code Code, msg, hint string) Outcome {
	return Outcome{Err: &ToolError{Code: code, Message: msg, Hint: hint}}
}

func messageOf(err error) string {
	if te, ok := errors.AsType[*ToolError](err); ok {
		return te.Message
	}
	return err.Error()
}

func hintOf(err error) string {
	if te, ok := errors.AsType[*ToolError](err); ok {
		return te.Hint
	}
	return ""
}

func validEffect(e Effect) bool {
	switch e {
	case EffectRead, EffectIdempotent, EffectNonIdempotent, effectDynamic:
		return true
	}
	return false
}

func wrapUntrusted(content, source string) string {
	content = escapeUntrustedClose(content)
	return `<untrusted source="` + source + `">` + content + "</untrusted>"
}

// escapeUntrustedClose neutralises every closing boundary tag in any
// capitalisation: `</UNTRUSTED>` would otherwise end the wrapper early and
// let the data issue instructions (SPEC §14.5, ADR-070; REVIEW-F1-SEC7
// TL7-2).
func escapeUntrustedClose(s string) string {
	const closeTag = "</untrusted"
	lower := strings.ToLower(s)
	if !strings.Contains(lower, closeTag) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for {
		i := strings.Index(lower, closeTag)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		b.WriteString(s[:i])
		b.WriteString(`<\\/untrusted`)
		s, lower = s[i+len(closeTag):], lower[i+len(closeTag):]
	}
}

// clipRunes returns the first n runes, never splitting a UTF-8 sequence.
func clipRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	i := 0
	for pos := range s {
		if i == n {
			return s[:pos]
		}
		i++
	}
	return s
}

// itoa avoids fmt in the hot render path.
func itoa(n int) string { return strconv.Itoa(n) }

func quote(s string) string { return `"` + s + `"` }

// Dispatch is the standalone convenience for frontends that delegate
// approval to the dispatcher: Prepare once, approve, then DispatchPrepared.
// The loop uses Prepare + its own Approver + DispatchPrepared instead.
func (d *Dispatcher) Dispatch(ctx context.Context, call Call) Outcome {
	p, err := d.Prepare(ctx, call)
	if err != nil {
		return fromError(err)
	}
	if fail := d.authorize(ctx, &p); fail != nil {
		return *fail
	}
	return d.DispatchPrepared(ctx, &p)
}

// authorize applies one prepared call's approval requirement. It is the only
// place that consults the approver or session approvals.
func (d *Dispatcher) authorize(ctx context.Context, p *Prepared) *Outcome {
	p.Env.Approved = false
	switch p.Decision.Requirement {
	case RequireAuto:
		p.authorized = true
		return nil
	case RequireDeniedPath:
		out := failure(CodeDeniedPath, "path matches deny_paths: "+strings.Join(p.Decision.Paths, ", "), "choose another path")
		return &out
	case RequireDeny:
		out := failure(CodeDenied, p.Call.Name+" is not allowed in this mode", "")
		out.Denied = true
		return &out
	case RequireOutside:
		if d.mode.Headless() {
			out := failure(CodeOutsideWorkspace, "path is outside the workspace: "+strings.Join(p.Decision.Paths, ", "), "headless runs never approve outside paths")
			return &out
		}
		return d.ask(ctx, p, true)
	case RequireAsk:
		return d.ask(ctx, p, false)
	}
	out := failure(CodeInternal, "unknown policy requirement", "")
	return &out
}

// ask consults a session approval or the approver for one prepared call.
func (d *Dispatcher) ask(ctx context.Context, p *Prepared, outside bool) *Outcome {
	dec := p.Decision
	if dec.AllowSession && d.sessionAllowed(p.Call.Name, dec.Paths) {
		p.Approved = outside
		p.authorized = true
		return nil
	}
	req := ApprovalRequest{
		Tool: p.Call.Name, CallID: p.Call.CallID, Effect: p.Effect, Class: dec.Class,
		Paths: dec.Paths, Preview: dec.Preview, Tainted: d.tainted.Load(),
		AllowSession: dec.AllowSession,
	}
	if d.approver == nil {
		out := failure(CodeDenied, "no approver is available", "interactive approval is required")
		out.Denied = true
		return &out
	}
	answer, err := d.approver.Approve(ctx, req)
	if err != nil {
		out := fromError(err)
		return &out
	}
	switch answer {
	case ApprovalAllowOnce:
		p.Approved = outside
		p.authorized = true
		return nil
	case ApprovalAllowSession:
		if req.AllowSession && len(req.Paths) > 0 {
			d.mu.Lock()
			for _, path := range req.Paths {
				d.sessions[toolSubject(req.Tool, path)] = true
			}
			d.mu.Unlock()
		}
		p.Approved = outside
		p.authorized = true
		return nil
	case ApprovalDeny:
	}
	code, msg := CodeDenied, "approval denied for "+p.Call.Name
	if outside {
		code, msg = CodeOutsideWorkspace, "outside-workspace access was not approved"
	}
	out := failure(code, msg, "")
	out.Denied = true
	return &out
}

// sessionAllowed reports an earlier allow_session for every exact subject.
func (d *Dispatcher) sessionAllowed(tool string, paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, p := range paths {
		if !d.sessions[toolSubject(tool, p)] {
			return false
		}
	}
	return true
}
