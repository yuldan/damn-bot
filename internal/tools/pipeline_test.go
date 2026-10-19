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
	"testing"
	"time"
)

// journalRec is the test double for the consumer-side Journal interface.
type journalRec struct {
	mu         sync.Mutex
	dispatches []journalDispatch
	results    []journalResult
	fail       bool
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
	if j.fail {
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

func codeOfOutcome(o Outcome) Code {
	if o.Err == nil {
		return ""
	}
	return o.Err.Code
}

func callOf(name string, args string) Call {
	return Call{TurnID: "t1", CallID: "c-" + name, Name: name, Args: json.RawMessage(args)}
}

func newStubDispatcher(t *testing.T, mode Mode, appr Approver, list ...Tool) *Dispatcher {
	t.Helper()
	d, err := NewDispatcher(Config{
		Registry: mustReg(t, list...), Journal: &journalRec{}, Spill: newMemSpill(),
		Mode: mode, Approver: appr, WorkspaceTrusted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDispatchSuccess(t *testing.T) {
	t.Parallel()
	tool := &stubTool{name: "echo", schema: echoSchema("echo"), effect: EffectRead,
		run: func(_ context.Context, _ Env, args json.RawMessage) (Result, error) {
			return Result{OK: true, Content: "hello", Meta: map[string]any{"x": 1}}, nil
		}}
	j := &journalRec{}
	d, err := NewDispatcher(Config{Registry: mustReg(t, tool), Journal: j, Spill: newMemSpill(), Mode: ModeAuto, WorkspaceTrusted: true})
	if err != nil {
		t.Fatal(err)
	}
	out := d.Dispatch(context.Background(), callOf("echo", `{"body":"hi"}`))
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
	d := newStubDispatcher(t, ModeAuto, nil, &stubTool{name: "echo", schema: echoSchema("echo"), effect: EffectRead})
	for _, name := range []string{"nope", `session_search" parameter=x`} {
		out := d.Dispatch(context.Background(), callOf(name, `{}`))
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
	d := newStubDispatcher(t, ModeAuto, nil, &stubTool{name: "echo", schema: echoSchema("echo"), effect: EffectRead})
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
			out := d.Dispatch(context.Background(), Call{Name: "echo", Args: json.RawMessage(c.args)})
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
	d := newStubDispatcher(t, ModeAuto, nil, tool)
	call := Call{TurnID: "t", CallID: "same", Name: "echo", Args: json.RawMessage(`{"body":"x"}`)}
	if out := d.Dispatch(context.Background(), call); out.Err != nil {
		t.Fatal(out.Err)
	}
	if out := d.Dispatch(context.Background(), call); codeOfOutcome(out) != CodeIdempotencyConflict {
		t.Fatalf("second code = %s", codeOfOutcome(out))
	}
	if tool.CallCount() != 1 {
		t.Fatalf("tool ran %d times, want 1", tool.CallCount())
	}
	d.BeginTurn()
	if out := d.Dispatch(context.Background(), Call{TurnID: "t2", CallID: "same", Name: "echo", Args: json.RawMessage(`{"body":"x"}`)}); out.Err != nil {
		t.Fatalf("new turn: %v", out.Err)
	}
}

func TestJournalRequired(t *testing.T) {
	t.Parallel()
	reg := mustReg(t, &stubTool{name: "echo", schema: echoSchema("echo"), effect: EffectRead})
	if _, err := NewDispatcher(Config{Registry: reg, Spill: newMemSpill()}); err == nil {
		t.Fatal("missing journal accepted")
	}
	if _, err := NewDispatcher(Config{Registry: reg, Journal: &journalRec{}}); err == nil {
		t.Fatal("missing spill accepted")
	}
	if _, err := NewDispatcher(Config{Journal: &journalRec{}, Spill: newMemSpill()}); err == nil {
		t.Fatal("missing registry accepted")
	}
}

func TestJournalFailureFailsClosed(t *testing.T) {
	t.Parallel()
	tool := &stubTool{name: "echo", schema: echoSchema("echo"), effect: EffectRead}
	j := &journalRec{fail: true}
	d, err := NewDispatcher(Config{Registry: mustReg(t, tool), Journal: j, Spill: newMemSpill(), Mode: ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	out := d.Dispatch(context.Background(), callOf("echo", `{"body":"x"}`))
	if codeOfOutcome(out) != CodeInternal {
		t.Fatalf("code = %s", codeOfOutcome(out))
	}
	if tool.CallCount() != 0 {
		t.Fatal("tool ran despite a failed journal dispatch")
	}
}

func TestPanicBecomesInternal(t *testing.T) {
	t.Parallel()
	tool := &stubTool{name: "boom", schema: echoSchema("boom"), effect: EffectRead,
		run: func(context.Context, Env, json.RawMessage) (Result, error) { panic("boom") }}
	d := newStubDispatcher(t, ModeAuto, nil, tool)
	out := d.Dispatch(context.Background(), callOf("boom", `{"body":"x"}`))
	if codeOfOutcome(out) != CodeInternal {
		t.Fatalf("code = %s", codeOfOutcome(out))
	}
}

func TestCancellation(t *testing.T) {
	t.Parallel()
	tool := &stubTool{name: "slow", schema: echoSchema("slow"), effect: EffectRead,
		run: func(ctx context.Context, _ Env, _ json.RawMessage) (Result, error) {
			<-ctx.Done()
			return Result{}, ctx.Err()
		}}
	d := newStubDispatcher(t, ModeAuto, nil, tool)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	out := d.Dispatch(ctx, callOf("slow", `{"body":"x"}`))
	if codeOfOutcome(out) != CodeTimeout || !out.Cancelled {
		t.Fatalf("code = %s cancelled = %v", codeOfOutcome(out), out.Cancelled)
	}
}

func TestInvalidEffectAndCode(t *testing.T) {
	t.Parallel()
	badEffect := &stubTool{name: "bad", schema: echoSchema("bad"), effect: Effect("weird")}
	d := newStubDispatcher(t, ModeAuto, nil, badEffect)
	if out := d.Dispatch(context.Background(), callOf("bad", `{"body":"x"}`)); codeOfOutcome(out) != CodeInternal {
		t.Fatalf("effect code = %s", codeOfOutcome(out))
	}
	badCode := &stubTool{name: "badcode", schema: echoSchema("badcode"), effect: EffectRead,
		run: func(context.Context, Env, json.RawMessage) (Result, error) {
			return Result{}, &ToolError{Code: Code("bogus"), Message: "x"}
		}}
	d2 := newStubDispatcher(t, ModeAuto, nil, badCode)
	if out := d2.Dispatch(context.Background(), callOf("badcode", `{"body":"x"}`)); codeOfOutcome(out) != CodeInternal {
		t.Fatalf("code closure failed: %s", codeOfOutcome(out))
	}
}

func TestDispatchAllocs(t *testing.T) {
	body := strings.Repeat("a", 1024)
	args := json.RawMessage(`{"body":"` + body + `"}`)
	tool := &stubTool{name: "echo", schema: echoSchema("echo"), effect: EffectRead,
		run: func(context.Context, Env, json.RawMessage) (Result, error) { return Result{OK: true}, nil }}
	j := &journalRec{}
	d, err := NewDispatcher(Config{Registry: mustReg(t, tool), Journal: j, Spill: newMemSpill(), Mode: ModeAuto})
	if err != nil {
		t.Fatal(err)
	}
	call := Call{Name: "echo", Args: args}
	allocs := testing.AllocsPerRun(1000, func() {
		if out := d.Dispatch(context.Background(), call); out.Err != nil {
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
	tool := &stubTool{name: "echo", schema: echoSchema("echo"), effect: EffectRead,
		run: func(context.Context, Env, json.RawMessage) (Result, error) { return Result{OK: true}, nil }}
	j := &journalRec{}
	d, err := NewDispatcher(Config{Registry: mustReg(b, tool), Journal: j, Spill: newMemSpill(), Mode: ModeAuto})
	if err != nil {
		b.Fatal(err)
	}
	call := Call{Name: "echo", Args: args}
	b.ReportAllocs()
	b.SetBytes(int64(len(args)))
	for b.Loop() {
		if out := d.Dispatch(context.Background(), call); out.Err != nil {
			b.Fatal(out.Err)
		}
	}
}
