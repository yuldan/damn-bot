package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// journalRec is the test double for the consumer-side Journal interface.
type journalRec struct {
	mu         sync.Mutex
	dispatches []journalDispatch
	results    []journalResult
	fail       bool
	failResult bool
}

type journalDispatch struct {
	turn, call, tool string
	effect           Effect
	hash             [32]byte
}

type journalResult struct {
	turn, call, tool, code string
	ok                     bool
	content                string
}

func (j *journalRec) ToolDispatch(_ context.Context, turn, call, tool string, effect Effect, hash [32]byte) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.fail {
		return errors.New("journal down")
	}
	j.dispatches = append(j.dispatches, journalDispatch{turn, call, tool, effect, hash})
	return nil
}

func (j *journalRec) ToolResult(_ context.Context, turn, call, tool string, ok bool, code, content string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.fail || j.failResult {
		return errors.New("journal down")
	}
	j.results = append(j.results, journalResult{turn, call, tool, code, ok, content})
	return nil
}

func (j *journalRec) counts() (int, int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.dispatches), len(j.results)
}

// memSpill is an in-memory Spill for pipeline tests.
type memSpill struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newMemSpill() *memSpill { return &memSpill{m: map[string][]byte{}} }

func (s *memSpill) Put(_ context.Context, data []byte) (BlobRef, error) {
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	s.mu.Lock()
	s.m[sha] = append([]byte(nil), data...)
	s.mu.Unlock()
	return BlobRef{SHA: sha, Size: int64(len(data)), Path: "/spill/" + sha}, nil
}

func (s *memSpill) Get(_ context.Context, sha string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[sha]
	if !ok {
		return nil, &ToolError{Code: CodeBlobMissing, Message: "missing", Err: os.ErrNotExist}
	}
	return b, nil
}

// approverRec records prompts and returns a fixed answer.
type approverRec struct {
	mu     sync.Mutex
	answer Approval
	err    error
	reqs   []ApprovalRequest
}

//nolint:gocritic // the Approver interface fixes the by-value request
func (a *approverRec) Approve(_ context.Context, req ApprovalRequest) (Approval, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reqs = append(a.reqs, req)
	if a.err != nil {
		return ApprovalDeny, a.err
	}
	return a.answer, nil
}

func (a *approverRec) requests() []ApprovalRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]ApprovalRequest(nil), a.reqs...)
}

// dispatchCall drives the split API the way the loop does: Prepare once,
// then DispatchPrepared. Approval tests use Dispatcher.Dispatch (approval
// layer) instead.
//
//nolint:gocritic // Call is a value carrier with a fixed public shape (ADR-198 added PlanMode)
func dispatchCall(ctx context.Context, d *Dispatcher, call Call) Outcome {
	p, err := d.Prepare(ctx, call)
	if err != nil {
		return fromError(err)
	}
	return d.DispatchPrepared(ctx, &p)
}

func codeOfOutcome(o Outcome) Code {
	if o.Err == nil {
		return ""
	}
	return o.Err.Code
}

func callOf(name string, args string) Call {
	return Call{TurnID: "t1", CallID: "c-" + name, Name: name, Args: json.RawMessage(args)}
}

func newStubDispatcher(t *testing.T, mode Mode, list ...Tool) *Dispatcher {
	t.Helper()
	d, err := NewDispatcher(&Config{
		Registry: mustReg(t, list...), Journal: &journalRec{}, Spill: newMemSpill(),
		Redactor: testRedactor(),
		Mode:     mode, WorkspaceTrusted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDispatchSuccess(t *testing.T) {
	t.Parallel()
	tool := &stubTool{
		name: "echo", schema: echoSchema("echo"), effect: EffectRead,
		run: func(_ context.Context, _ Env, args json.RawMessage) (Result, error) {
			return Result{OK: true, Content: "hello", Meta: map[string]any{"x": 1}}, nil
		},
	}
	j := &journalRec{}
	d, err := NewDispatcher(&Config{Registry: mustReg(t, tool), Journal: j, Spill: newMemSpill(), Redactor: testRedactor(), Mode: ModeAuto, WorkspaceTrusted: true})
	if err != nil {
		t.Fatal(err)
	}
	out := dispatchCall(context.Background(), d, callOf("echo", `{"body":"hi"}`))
	if out.Err != nil {
		t.Fatalf("dispatch: %v", out.Err)
	}
	if out.Content != "hello" || out.Render() != "hello" {
		t.Fatalf("content = %q", out.Content)
	}
	dc, rc := j.counts()
	if dc != 1 || rc != 1 {
		t.Fatalf("journal calls = %d/%d, want 1/1", dc, rc)
	}
	if j.dispatches[0].tool != "echo" || j.dispatches[0].effect != EffectRead {
		t.Fatalf("dispatch row = %+v", j.dispatches[0])
	}
	if !j.results[0].ok || j.results[0].content != "hello" {
		t.Fatalf("result row = %+v", j.results[0])
	}
}

func TestUnknownTool(t *testing.T) {
	t.Parallel()
	d := newStubDispatcher(t, ModeAuto, &stubTool{name: "echo", schema: echoSchema("echo"), effect: EffectRead})
	for _, name := range []string{"nope", `session_search" parameter=x`} {
		out := dispatchCall(context.Background(), d, callOf(name, `{}`))
		if codeOfOutcome(out) != CodeUnknownTool {
			t.Fatalf("%s: code = %s, want unknown_tool", name, codeOfOutcome(out))
		}
		if !strings.Contains(out.Render(), `"ok":false`) {
			t.Fatalf("error envelope = %s", out.Render())
		}
	}
}

func TestInvalidArgs(t *testing.T) {
	t.Parallel()
	d := newStubDispatcher(t, ModeAuto, &stubTool{name: "echo", schema: echoSchema("echo"), effect: EffectRead})
	cases := []struct {
		name string
		args string
		want string
	}{
		{"missing", `{}`, "body"},
		{"wrong type", `{"body":1}`, "body"},
		{"unknown field", `{"body":"x","extra":1}`, "extra"},
		{"trailing garbage", `{"body":"x"} tail`, "byte"},
		{"incomplete", `{"body":`, "byte"},
		{"duplicate key", `{"body":"x","body":"y"}`, "duplicate"},
		{"not object", `[]`, ""},
		{"null root", `null`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := dispatchCall(context.Background(), d, Call{Name: "echo", Args: json.RawMessage(c.args)})
			if codeOfOutcome(out) != CodeInvalidArgs {
				t.Fatalf("code = %s, want invalid_args (%s)", codeOfOutcome(out), out.Render())
			}
			if c.want != "" && !strings.Contains(out.Render(), c.want) {
				t.Fatalf("envelope %s does not mention %q", out.Render(), c.want)
			}
		})
	}
}

func TestDuplicateCallID(t *testing.T) {
	t.Parallel()
	tool := &stubTool{name: "echo", schema: echoSchema("echo"), effect: EffectRead}
	d := newStubDispatcher(t, ModeAuto, tool)
	call := Call{TurnID: "t", CallID: "same", Name: "echo", Args: json.RawMessage(`{"body":"x"}`)}
	if out := dispatchCall(context.Background(), d, call); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := dispatchCall(context.Background(), d, call); codeOfOutcome(out) != CodeIdempotencyConflict {
		t.Fatalf("second code = %s", codeOfOutcome(out))
	}
	if tool.CallCount() != 1 {
		t.Fatalf("tool ran %d times, want 1", tool.CallCount())
	}
	d.BeginTurn()
	if out := dispatchCall(context.Background(), d, Call{TurnID: "t2", CallID: "same", Name: "echo", Args: json.RawMessage(`{"body":"x"}`)}); out.Err != nil {
		t.Fatalf("new turn: %v", out.Err)
	}
}

func TestJournalRequired(t *testing.T) {
	t.Parallel()
	reg := mustReg(t, &stubTool{name: "echo", schema: echoSchema("echo"), effect: EffectRead})
	if _, err := NewDispatcher(&Config{Registry: reg, Spill: newMemSpill()}); err == nil {
		t.Fatal("missing journal accepted")
	}
	if _, err := NewDispatcher(&Config{Registry: reg, Journal: &journalRec{}}); err == nil {
		t.Fatal("missing spill accepted")
	}
	if _, err := NewDispatcher(&Config{Journal: &journalRec{}, Spill: newMemSpill()}); err == nil {
		t.Fatal("missing registry accepted")
	}
	if _, err := NewDispatcher(&Config{Registry: reg, Journal: &journalRec{}, Spill: newMemSpill()}); err == nil {
		t.Fatal("missing redactor accepted")
	}
}

func TestJournalFailureFailsClosed(t *testing.T) {
	t.Parallel()
	tool := &stubTool{name: "echo", schema: echoSchema("echo"), effect: EffectRead}
	j := &journalRec{fail: true}
	d, err := NewDispatcher(&Config{Registry: mustReg(t, tool), Journal: j, Spill: newMemSpill(), Redactor: testRedactor(), Mode: ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	out := dispatchCall(context.Background(), d, callOf("echo", `{"body":"x"}`))
	if codeOfOutcome(out) != CodeInternal {
		t.Fatalf("code = %s", codeOfOutcome(out))
	}
	if tool.CallCount() != 0 {
		t.Fatal("tool ran despite a failed journal dispatch")
	}
}

func TestCancellation(t *testing.T) {
	t.Parallel()
	tool := &stubTool{
		name: "slow", schema: echoSchema("slow"), effect: EffectRead,
		run: func(ctx context.Context, _ Env, _ json.RawMessage) (Result, error) {
			<-ctx.Done()
			return Result{}, ctx.Err()
		},
	}
	d := newStubDispatcher(t, ModeAuto, tool)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	out := dispatchCall(ctx, d, callOf("slow", `{"body":"x"}`))
	if codeOfOutcome(out) != CodeTimeout || !out.Cancelled {
		t.Fatalf("code = %s cancelled = %v", codeOfOutcome(out), out.Cancelled)
	}
}

func TestInvalidEffectAndCode(t *testing.T) {
	t.Parallel()
	badEffect := &stubTool{name: "bad", schema: echoSchema("bad"), effect: Effect("weird")}
	d := newStubDispatcher(t, ModeAuto, badEffect)
	if out := dispatchCall(context.Background(), d, callOf("bad", `{"body":"x"}`)); codeOfOutcome(out) != CodeInternal {
		t.Fatalf("effect code = %s", codeOfOutcome(out))
	}
	badCode := &stubTool{
		name: "badcode", schema: echoSchema("badcode"), effect: EffectRead,
		run: func(context.Context, Env, json.RawMessage) (Result, error) {
			return Result{}, &ToolError{Code: Code("bogus"), Message: "x"}
		},
	}
	d2 := newStubDispatcher(t, ModeAuto, badCode)
	if out := dispatchCall(context.Background(), d2, callOf("badcode", `{"body":"x"}`)); codeOfOutcome(out) != CodeInternal {
		t.Fatalf("code closure failed: %s", codeOfOutcome(out))
	}
}

func TestDispatchAllocs(t *testing.T) {
	body := strings.Repeat("a", 1024)
	args := json.RawMessage(`{"body":"` + body + `"}`)
	tool := &stubTool{
		name: "echo", schema: echoSchema("echo"), effect: EffectRead,
		run: func(context.Context, Env, json.RawMessage) (Result, error) { return Result{OK: true}, nil },
	}
	j := &journalRec{}
	d, err := NewDispatcher(&Config{Registry: mustReg(t, tool), Journal: j, Spill: newMemSpill(), Redactor: testRedactor(), Mode: ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	call := Call{Name: "echo", Args: args}
	allocs := testing.AllocsPerRun(1000, func() {
		if out := dispatchCall(context.Background(), d, call); out.Err != nil {
			t.Fatalf("dispatch: %v", out.Err)
		}
	})
	if allocs > 2 {
		t.Fatalf("dispatch allocated %.1f times, want ≤ 2 (E-37)", allocs)
	}
}

func BenchmarkDispatch(b *testing.B) {
	body := strings.Repeat("a", 1024)
	args := json.RawMessage(`{"body":"` + body + `"}`)
	tool := &stubTool{
		name: "echo", schema: echoSchema("echo"), effect: EffectRead,
		run: func(context.Context, Env, json.RawMessage) (Result, error) { return Result{OK: true}, nil },
	}
	j := &journalRec{}
	d, err := NewDispatcher(&Config{Registry: mustReg(b, tool), Journal: j, Spill: newMemSpill(), Redactor: testRedactor(), Mode: ModeAuto})
	if err != nil {
		b.Fatal(err)
	}
	call := Call{Name: "echo", Args: args}
	b.ReportAllocs()
	b.SetBytes(int64(len(args)))
	for b.Loop() {
		if out := dispatchCall(context.Background(), d, call); out.Err != nil {
			b.Fatal(out.Err)
		}
	}
}

func TestPrepareDoesNotApproveOrRun(t *testing.T) {
	t.Parallel()
	var inspects atomic.Int32
	tool := &stubTool{name: "read", schema: echoSchema("read"), kind: KindRead, effect: EffectRead}
	tool.in = func(context.Context, Env, json.RawMessage, PolicyContext) (PolicyDecision, error) {
		inspects.Add(1)
		return PolicyDecision{Requirement: RequireAuto, Class: "read"}, nil
	}
	j := &journalRec{}
	appr := &approverRec{answer: ApprovalAllowOnce}
	d, err := NewDispatcher(&Config{Registry: mustReg(t, tool), Journal: j, Spill: newMemSpill(), Redactor: testRedactor(), Mode: ModeAsk, Approver: appr, WorkspaceTrusted: true})
	if err != nil {
		t.Fatal(err)
	}
	p, err := d.Prepare(context.Background(), Call{TurnID: "t", CallID: "c", Name: "read", Args: json.RawMessage(`{"body":"x"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if p.Effect != EffectRead || inspects.Load() != 1 {
		t.Fatalf("prepared = %+v inspects=%d", p, inspects.Load())
	}
	if tool.CallCount() != 0 || len(appr.requests()) != 0 {
		t.Fatal("Prepare ran the tool or the approver")
	}
	if dc, rc := j.counts(); dc != 0 || rc != 0 {
		t.Fatalf("Prepare journaled %d/%d", dc, rc)
	}
	out := d.DispatchPrepared(context.Background(), &p)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if tool.CallCount() != 1 || inspects.Load() != 1 {
		t.Fatalf("DispatchPrepared ran tool=%d inspects=%d; policy must run once", tool.CallCount(), inspects.Load())
	}
	if len(appr.requests()) != 0 {
		t.Fatal("DispatchPrepared consulted the approver")
	}
	if dc, rc := j.counts(); dc != 1 || rc != 1 {
		t.Fatalf("journal = %d/%d", dc, rc)
	}
}

func TestPrepareExposesApprovalDecision(t *testing.T) {
	t.Parallel()
	tool := &stubTool{name: "write", schema: echoSchema("write"), kind: KindEdit, effect: EffectIdempotent}
	tool.in = func(_ context.Context, _ Env, _ json.RawMessage, _ PolicyContext) (PolicyDecision, error) {
		return PolicyDecision{Requirement: RequireAsk, Paths: []string{"/w/a"}, Preview: "p", Class: "write", AllowSession: true}, nil
	}
	d := newStubDispatcher(t, ModeAsk, tool)
	p, err := d.Prepare(context.Background(), Call{Name: "write", Args: json.RawMessage(`{"body":"x"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if p.Decision.Requirement != RequireAsk || !p.Decision.AllowSession || len(p.Decision.Paths) != 1 {
		t.Fatalf("decision = %+v", p.Decision)
	}
	if p.Approved {
		t.Fatal("Approved must be false before the caller approves")
	}
}

func TestTaintedStateAndGate(t *testing.T) {
	t.Parallel()
	write := &stubTool{name: "write", schema: echoSchema("write"), effect: EffectIdempotent}
	d := newStubDispatcher(t, ModeAuto, write)
	if d.Tainted() {
		t.Fatal("fresh dispatcher tainted")
	}
	d.MarkTainted()
	if !d.Tainted() {
		t.Fatal("MarkTainted did not stick")
	}
	p, err := d.Prepare(context.Background(), Call{Name: "write", Args: json.RawMessage(`{"body":"x"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if p.Decision.Requirement != RequireAsk || p.Decision.AllowSession {
		t.Fatalf("tainted decision = %+v", p.Decision)
	}
	dh := newStubDispatcher(t, ModeHeadless, write)
	dh.MarkTainted()
	ph, err := dh.Prepare(context.Background(), Call{Name: "write", Args: json.RawMessage(`{"body":"x"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if ph.Decision.Requirement != RequireDeny {
		t.Fatalf("headless tainted = %v", ph.Decision.Requirement)
	}
	d.BeginTurn()
	if d.Tainted() {
		t.Fatal("BeginTurn kept taint")
	}
}

func TestFinishUntrustedAndSpill(t *testing.T) {
	t.Parallel()
	tool := &stubTool{
		name: "fetch", schema: echoSchema("fetch"), kind: KindRead, effect: EffectRead,
		run: func(context.Context, Env, json.RawMessage) (Result, error) {
			return Result{OK: true, Content: strings.Repeat("x", 10000), UntrustedSource: "file:outside_workspace"}, nil
		},
	}
	d := newStubDispatcher(t, ModeAuto, tool)
	p, err := d.Prepare(context.Background(), Call{Name: "fetch", Args: json.RawMessage(`{"body":"x"}`)})
	if err != nil {
		t.Fatal(err)
	}
	out := d.DispatchPrepared(context.Background(), &p)
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if !out.Tainted || !strings.HasPrefix(out.Content, `<untrusted source="file:outside_workspace">`) {
		t.Fatalf("content = %q tainted=%v", out.Content, out.Tainted)
	}
	if out.Spilled == nil {
		t.Fatal("wrapped oversized content was not spilled")
	}
}

// TestWorkspaceSourceOnlyFillsBlank: an untrusted workspace labels reads
// "workspace", but only when the tool left the source blank. A tool that
// already declared its own source keeps it (session_search's "session" is the
// SPEC §14.5 case; the fallback used to overwrite it).
func TestWorkspaceSourceOnlyFillsBlank(t *testing.T) {
	t.Parallel()
	declared := &stubTool{
		name: "session_search", schema: echoSchema("session_search"), kind: KindRead, effect: EffectRead,
		run: func(context.Context, Env, json.RawMessage) (Result, error) {
			return Result{OK: true, Content: "hit", UntrustedSource: "session"}, nil
		},
	}
	blank := &stubTool{name: "read_file", schema: echoSchema("read_file"), kind: KindRead, effect: EffectRead}
	d, err := NewDispatcher(&Config{
		Registry: mustReg(t, declared, blank), Journal: &journalRec{}, Spill: newMemSpill(),
		Redactor: testRedactor(), Mode: ModeAuto, WorkspaceTrusted: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := dispatchCall(context.Background(), d, callOf("session_search", `{"body":"x"}`))
	if !out.Tainted || !strings.HasPrefix(out.Content, `<untrusted source="session">`) {
		t.Fatalf("declared source = %q tainted=%v, want the tool's own label", out.Content, out.Tainted)
	}
	ctl := dispatchCall(context.Background(), d, callOf("read_file", `{"body":"x"}`))
	if !ctl.Tainted || !strings.HasPrefix(ctl.Content, `<untrusted source="workspace">`) {
		t.Fatalf("blank source = %q tainted=%v, want the workspace fallback", ctl.Content, ctl.Tainted)
	}
}

func TestPagedToolNoSpill(t *testing.T) {
	t.Parallel()
	tool := &stubTool{
		name: "read_file", schema: echoSchema("read_file"), kind: KindRead, effect: EffectRead,
		run: func(context.Context, Env, json.RawMessage) (Result, error) {
			return Result{OK: true, Content: strings.Repeat("x", 5000)}, nil
		},
	}
	d := newStubDispatcher(t, ModeAuto, tool)
	p, err := d.Prepare(context.Background(), Call{Name: "read_file", Args: json.RawMessage(`{"body":"x"}`)})
	if err != nil {
		t.Fatal(err)
	}
	out := d.DispatchPrepared(context.Background(), &p)
	if out.Spilled != nil || len(out.Content) != 5000 {
		t.Fatalf("paged output spilled or truncated: %d", len(out.Content))
	}
}

func TestDispatchPreparedUnprepared(t *testing.T) {
	t.Parallel()
	d := newStubDispatcher(t, ModeAuto, &stubTool{name: "x", schema: echoSchema("x"), effect: EffectRead})
	if out := d.DispatchPrepared(context.Background(), nil); codeOfOutcome(out) != CodeInternal {
		t.Fatalf("nil prepared code = %s", codeOfOutcome(out))
	}
}

func TestInspectorError(t *testing.T) {
	t.Parallel()
	tool := &stubTool{name: "x", schema: echoSchema("x"), effect: EffectRead}
	tool.in = func(context.Context, Env, json.RawMessage, PolicyContext) (PolicyDecision, error) {
		return PolicyDecision{}, errors.New("boom")
	}
	d := newStubDispatcher(t, ModeAuto, tool)
	if _, err := d.Prepare(context.Background(), Call{Name: "x", Args: json.RawMessage(`{"body":"x"}`)}); codeOf(err) != CodeInternal {
		t.Fatalf("code = %v", err)
	}
}

func TestToolErrorPreserved(t *testing.T) {
	t.Parallel()
	tool := &stubTool{
		name: "x", schema: echoSchema("x"), effect: EffectRead,
		run: func(context.Context, Env, json.RawMessage) (Result, error) {
			return Result{}, &ToolError{Code: CodeNoMatch, Message: "none"}
		},
	}
	d := newStubDispatcher(t, ModeAuto, tool)
	p, err := d.Prepare(context.Background(), Call{Name: "x", Args: json.RawMessage(`{"body":"x"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if out := d.DispatchPrepared(context.Background(), &p); codeOfOutcome(out) != CodeNoMatch {
		t.Fatalf("code = %s", codeOfOutcome(out))
	}
}

func TestJournalResultFailure(t *testing.T) {
	t.Parallel()
	tool := &stubTool{name: "x", schema: echoSchema("x"), effect: EffectRead}
	j := &journalRec{failResult: true}
	d, err := NewDispatcher(&Config{Registry: mustReg(t, tool), Journal: j, Spill: newMemSpill(), Redactor: testRedactor(), Mode: ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	out := dispatchCall(context.Background(), d, Call{Name: "x", Args: json.RawMessage(`{"body":"x"}`)})
	if out.Err != nil {
		t.Fatalf("result journal failure must not fail the call: %v", out.Err)
	}
	if dc, rc := j.counts(); dc != 1 || rc != 0 {
		t.Fatalf("journal = %d/%d", dc, rc)
	}
}

// A tool that reports failure through Result.Err (no Go error) must not
// render as success (REVIEW-F1-SEC7 TL7-1).
func TestToolReportedFailureIsRendered(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		res  Result
		want Code
	}{
		{"err", Result{Err: &ToolError{Code: CodeDenied, Message: "not allowed"}}, CodeDenied},
		{"silent", Result{}, CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tool := &stubTool{
				name: "echo", schema: echoSchema("echo"), effect: EffectRead,
				run: func(context.Context, Env, json.RawMessage) (Result, error) { return tc.res, nil },
			}
			d := newStubDispatcher(t, ModeAuto, tool)
			out := dispatchCall(context.Background(), d, callOf("echo", `{"body":"hi"}`))
			if out.Err == nil || out.Err.Code != tc.want {
				t.Fatalf("out.Err = %+v, want %s", out.Err, tc.want)
			}
			if got := out.Render(); !strings.Contains(got, `"ok":false`) || !strings.Contains(got, string(tc.want)) {
				t.Fatalf("Render = %q", got)
			}
		})
	}
}

// The untrusted boundary must survive any capitalisation of its closing tag
// (REVIEW-F1-SEC7 TL7-2).
func TestUntrustedBoundaryCaseInsensitive(t *testing.T) {
	t.Parallel()
	for _, in := range []string{
		"</untrusted>", "</UNTRUSTED>", "</Untrusted foo", "a</untrusted\tb",
		`<untrusted source="x">nested</untrusted>`,
	} {
		wrapped := wrapUntrusted(in, "workspace")
		if n := strings.Count(strings.ToLower(wrapped), "</untrusted"); n != 1 {
			t.Fatalf("%q: %d closing tags in %q", in, n, wrapped)
		}
		if !strings.HasSuffix(wrapped, "</untrusted>") {
			t.Fatalf("%q: wrapper does not end with its own tag: %q", in, wrapped)
		}
	}
}

func TestDispatchApprovesExactlyOnce(t *testing.T) {
	t.Parallel()
	tool := &stubTool{name: "write", schema: echoSchema("write"), kind: KindEdit, effect: EffectIdempotent}
	appr := &approverRec{answer: ApprovalAllowOnce}
	d, err := NewDispatcher(&Config{
		Registry: mustReg(t, tool), Journal: &journalRec{}, Spill: newMemSpill(),
		Redactor: testRedactor(),
		Mode:     ModeAsk, Approver: appr, WorkspaceTrusted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := d.Dispatch(context.Background(), callOf("write", `{"body":"x"}`))
	if out.Err != nil {
		t.Fatal(out.Err)
	}
	if tool.CallCount() != 1 || len(appr.requests()) != 1 {
		t.Fatalf("tool=%d prompts=%d", tool.CallCount(), len(appr.requests()))
	}
}

// A prepared call must not execute without approval, and it must execute at
// most once (REVIEW-F1-SEC7 TL7-11).
func TestDispatchPreparedDeniesAndIsSingleUse(t *testing.T) {
	t.Parallel()
	tool := &stubTool{name: "write", schema: echoSchema("write"), kind: KindEdit, effect: EffectIdempotent}
	appr := &approverRec{answer: ApprovalDeny}
	d, err := NewDispatcher(&Config{
		Registry: mustReg(t, tool), Journal: &journalRec{}, Spill: newMemSpill(),
		Redactor: testRedactor(),
		Mode:     ModeAsk, Approver: appr, WorkspaceTrusted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := d.Prepare(context.Background(), callOf("write", `{"body":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out := d.DispatchPrepared(context.Background(), &p); out.Err == nil || !out.Denied {
		t.Fatalf("denied prepared call ran: %+v", out.Err)
	}
	if tool.CallCount() != 0 {
		t.Fatalf("tool ran %d times without approval", tool.CallCount())
	}
	p.Approved = true
	if out := d.DispatchPrepared(context.Background(), &p); out.Err != nil {
		t.Fatalf("approved prepared call: %v", out.Err)
	}
	if out := d.DispatchPrepared(context.Background(), &p); out.Err == nil {
		t.Fatal("prepared call executed twice")
	}
	if tool.CallCount() != 1 {
		t.Fatalf("tool ran %d times", tool.CallCount())
	}
}
