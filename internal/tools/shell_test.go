package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/exec/classify"
	"github.com/damnbot/damnbot/internal/redact"
)

func testShell(t *testing.T, w *workspace) (Tool, func()) {
	return testShellTrusted(t, w, true)
}

func testShellTrusted(t *testing.T, w *workspace, trusted bool) (Tool, func()) {
	t.Helper()
	runner := exec.New(t.Context())
	sh := NewShell(&ShellConfig{
		Runner: runner, Spill: newMemSpill(), Redactor: redact.New(redact.Options{}),
		Roots: []string{w.root}, Home: w.root, Cwd: w.root,
		ParentEnv: os.Environ(), EnvPolicy: &exec.EnvPolicy{},
		WorkspaceTrusted: trusted,
	})
	return sh, func() {
		if err := runner.Close(); err != nil {
			t.Error(err)
		}
	}
}

func TestApprovalModesShell(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	sh, closeFn := testShell(t, w)
	defer closeFn()
	appr := &approverRec{answer: ApprovalAllowOnce}
	d, _, _ := w.dispatcher(t, ModeAsk, appr, sh)
	out := d.Dispatch(context.Background(), callOf("shell", `{"command":"echo hi"}`))
	if out.Err != nil {
		t.Fatalf("shell: %v", out.Err)
	}
	if !strings.Contains(out.Content, "hi") || !strings.Contains(out.Content, "status exited") {
		t.Fatalf("content = %q", out.Content)
	}
	reqs := appr.requests()
	if len(reqs) != 1 {
		t.Fatalf("shell prompts = %d", len(reqs))
	}
	if reqs[0].AllowSession || !strings.HasPrefix(reqs[0].Class, "shell:") {
		t.Fatalf("shell request = %+v", reqs[0])
	}
}

func TestShellHeadlessDenied(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	sh, closeFn := testShell(t, w)
	defer closeFn()
	appr := &approverRec{answer: ApprovalAllowOnce}
	d, _, _ := w.dispatcher(t, ModeHeadless, appr, sh)
	out := d.Dispatch(context.Background(), callOf("shell", `{"command":"echo hi"}`))
	if codeOfOutcome(out) != CodeDenied {
		t.Fatalf("code = %s", codeOfOutcome(out))
	}
	if len(appr.requests()) != 0 {
		t.Fatal("headless must not prompt")
	}
}

func TestShellAllowExact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX argv binary")
	}
	t.Parallel()
	w := newWorkspace(t)
	runner := newTestRunner(t.Context())
	defer func() {
		if err := runner.Close(); err != nil {
			t.Error(err)
		}
	}()
	start := map[string]classify.BinaryID{"echo": {Path: "/bin/echo", Dev: 1, Ino: 2, Size: 3, MtimeNs: 4}}
	sh := NewShell(&ShellConfig{
		Runner: runner, Spill: newMemSpill(), Roots: []string{w.root}, Home: w.root, Cwd: w.root,
		ParentEnv: os.Environ(), EnvPolicy: testEnvPolicy(),
		Allow:        []classify.AllowEntry{{"echo", "hi"}},
		SessionStart: start,
		Resolve: func(argv0 string) (classify.BinaryID, bool) {
			id, ok := start[argv0]
			return id, ok
		},
		WorkspaceTrusted: true,
	})
	appr := &approverRec{answer: ApprovalDeny}
	d, _, _ := w.dispatcher(t, ModeHeadless, appr, sh)
	out := d.Dispatch(context.Background(), callOf("shell", `{"command":"echo hi"}`))
	if out.Err != nil {
		t.Fatalf("shell_allow exact match must run headless: %v", out.Err)
	}
	if len(appr.requests()) != 0 {
		t.Fatal("shell_allow auto path prompted")
	}
}

func TestShellAllowNonMatchAsks(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	sh, closeFn := testShell(t, w)
	defer closeFn()
	appr := &approverRec{answer: ApprovalAllowOnce}
	d, _, _ := w.dispatcher(t, ModeAuto, appr, sh)
	out := d.Dispatch(context.Background(), callOf("shell", `{"command":"echo hi"}`))
	if out.Err != nil {
		t.Fatalf("shell: %v", out.Err)
	}
	if len(appr.requests()) != 1 {
		t.Fatalf("expected one prompt, got %d", len(appr.requests()))
	}
}

func TestShellSessionStdinRejected(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	sh, closeFn := testShell(t, w)
	defer closeFn()
	d, _, _ := w.dispatcher(t, ModeAuto, &approverRec{answer: ApprovalAllowOnce}, sh)
	out := d.Dispatch(context.Background(), callOf("shell", `{"command":"cat","session":"s1","stdin":"x"}`))
	if codeOfOutcome(out) != CodeInvalidArgs {
		t.Fatalf("code = %s", codeOfOutcome(out))
	}
}

func TestShellBackgroundRejected(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	sh, closeFn := testShell(t, w)
	defer closeFn()
	d, _, _ := w.dispatcher(t, ModeAuto, &approverRec{answer: ApprovalAllowOnce}, sh)
	out := d.Dispatch(context.Background(), callOf("shell", `{"command":"echo hi","background":true}`))
	if codeOfOutcome(out) != CodeInvalidArgs {
		t.Fatalf("code = %s (%s)", codeOfOutcome(out), out.Render())
	}
}

func TestShellOutputUntrustedWorkspace(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	sh, closeFn := testShellTrusted(t, w, false)
	defer closeFn()
	appr := &approverRec{answer: ApprovalAllowOnce}
	reg := mustReg(t, append(w.tools, sh)...)
	d, err := NewDispatcher(&Config{
		Registry: reg, Journal: &journalRec{}, Spill: newMemSpill(), Redactor: testRedactor(), Mode: ModeAsk,
		Approver: appr, WorkspaceTrusted: false, Cwd: w.root,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := d.Dispatch(context.Background(), callOf("shell", `{"command":"echo hi"}`))
	if out.Err != nil {
		t.Fatalf("shell: %v", out.Err)
	}
	if !strings.HasPrefix(out.Content, `<untrusted source="workspace">`) || !out.Tainted {
		t.Fatalf("content = %q tainted=%v", out.Content, out.Tainted)
	}
}

func newTestRunner(ctx context.Context) *exec.Runner { return exec.New(ctx) }

func testEnvPolicy() *exec.EnvPolicy { return &exec.EnvPolicy{} }

// fakeRunner records the spec it is given and never spawns anything.
type fakeRunner struct {
	calls int
	spec  exec.Spec
}

func (f *fakeRunner) Run(_ context.Context, spec *exec.Spec) (exec.Result, error) {
	f.calls++
	f.spec = *spec
	code := 0
	return exec.Result{Status: exec.StatusExited, ExitCode: &code}, nil
}

// fakeShell builds a shell tool over fakeRunner with the given allow list.
func fakeShell(t *testing.T, w *workspace, runner *fakeRunner, allow []classify.AllowEntry, resolve func(string) (classify.BinaryID, bool)) Tool {
	t.Helper()
	id := classify.BinaryID{Path: "/bin/echo", Dev: 1, Ino: 2, Size: 3, MtimeNs: 4}
	return NewShell(&ShellConfig{
		Runner: runner, Spill: newMemSpill(), Redactor: redact.New(redact.Options{}),
		Roots: []string{w.root}, Home: w.root, Cwd: w.root,
		ParentEnv: os.Environ(), EnvPolicy: testEnvPolicy(),
		Allow: allow, SessionStart: map[string]classify.BinaryID{"echo": id},
		Resolve: resolve, WorkspaceTrusted: true,
	})
}

// A shell_allow match that vanishes between Inspect and Run must never fall
// back to shell mode (REVIEW-F1-SEC7 TL7-4).
func TestShellAllowTOCTOUFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX argv binary")
	}
	t.Parallel()
	w := newWorkspace(t)
	id := classify.BinaryID{Path: "/bin/echo", Dev: 1, Ino: 2, Size: 3, MtimeNs: 4}
	var resolves atomic.Int32
	runner := &fakeRunner{}
	sh := fakeShell(t, w, runner, []classify.AllowEntry{{"echo", "hi"}}, func(string) (classify.BinaryID, bool) {
		if resolves.Add(1) == 1 {
			return id, true
		}
		return classify.BinaryID{}, false
	})
	in, ok := sh.(Inspector)
	if !ok {
		t.Fatal("shell tool does not implement Inspector")
	}
	env := Env{CallID: "c1", Cwd: w.root, InlineLeft: TurnInlineChars}
	args := json.RawMessage(`{"command":"echo hi"}`)
	dec, err := in.Inspect(context.Background(), env, args, PolicyContext{Mode: ModeHeadless, WorkspaceTrusted: true})
	if err != nil || dec.Requirement != RequireAuto {
		t.Fatalf("inspect = %+v, %v", dec, err)
	}
	if _, err := sh.Run(context.Background(), env, args); codeOf(err) != CodeDenied {
		t.Fatalf("code = %q (%v), want denied", codeOf(err), err)
	}
	if runner.calls != 0 {
		t.Fatalf("runner ran %d times after the allow match changed", runner.calls)
	}
}

// Inspect records only an approved direct argv: a shell-mode call has no
// execution mode to protect and must not leave a record behind
// (REVIEW-F1-SEC7 TL7-15).
func TestShellInspectWithoutAllowLeavesNoRecord(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id := classify.BinaryID{Path: "/bin/echo", Dev: 1, Ino: 2, Size: 3, MtimeNs: 4}
	sh := fakeShell(t, w, &fakeRunner{}, []classify.AllowEntry{{"echo", "hi"}}, func(string) (classify.BinaryID, bool) {
		return id, true
	})
	in, ok := sh.(Inspector)
	if !ok {
		t.Fatal("shell tool does not implement Inspector")
	}
	st, ok := sh.(*shellTool)
	if !ok {
		t.Fatal("shell tool is not *shellTool")
	}
	env := Env{CallID: "c1", Cwd: w.root, InlineLeft: TurnInlineChars}
	args := json.RawMessage(`{"command":"echo bye"}`)
	if _, err := in.Inspect(context.Background(), env, args, PolicyContext{Mode: ModeAsk, WorkspaceTrusted: true}); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	st.mu.Lock()
	n := len(st.rec)
	st.mu.Unlock()
	if n != 0 {
		t.Fatalf("records after a non-matching Inspect = %d, want 0", n)
	}
}

// A shell_allow match that appears only at Run was never approved and must
// fail closed instead of running argv the approval never saw
// (REVIEW-F1-SEC7 TL7-4, TL7-15).
func TestShellAllowAppearsAfterInspectFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX argv binary")
	}
	t.Parallel()
	w := newWorkspace(t)
	id := classify.BinaryID{Path: "/bin/echo", Dev: 1, Ino: 2, Size: 3, MtimeNs: 4}
	var resolvable atomic.Bool
	runner := &fakeRunner{}
	sh := fakeShell(t, w, runner, []classify.AllowEntry{{"echo", "hi"}}, func(string) (classify.BinaryID, bool) {
		return id, resolvable.Load()
	})
	in, ok := sh.(Inspector)
	if !ok {
		t.Fatal("shell tool does not implement Inspector")
	}
	env := Env{CallID: "c1", Cwd: w.root, InlineLeft: TurnInlineChars}
	args := json.RawMessage(`{"command":"echo hi"}`)
	if _, err := in.Inspect(context.Background(), env, args, PolicyContext{Mode: ModeAsk, WorkspaceTrusted: true}); err != nil {
		t.Fatalf("inspect: %v", err)
	}
	resolvable.Store(true)
	if _, err := sh.Run(context.Background(), env, args); codeOf(err) != CodeDenied {
		t.Fatalf("code = %q (%v), want denied", codeOf(err), err)
	}
	if runner.calls != 0 {
		t.Fatalf("runner ran %d times for an unapproved shell_allow match", runner.calls)
	}
}

// A cwd outside the workspace is refused instead of being passed to the child
// (REVIEW-F1-SEC7 TL7-5).
func TestShellCwdOutsideWorkspaceDenied(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	runner := &fakeRunner{}
	st := NewShell(&ShellConfig{
		Runner: runner, Spill: newMemSpill(), Redactor: redact.New(redact.Options{}),
		Roots: []string{w.root}, Home: w.root, Cwd: w.root,
		ParentEnv: os.Environ(), EnvPolicy: testEnvPolicy(), WorkspaceTrusted: true,
	})
	args := json.RawMessage(`{"command":"echo hi","cwd":` + strconv.Quote(filepath.Dir(w.root)) + `}`)
	in, ok := st.(Inspector)
	if !ok {
		t.Fatal("shell tool does not implement Inspector")
	}
	if _, err := in.Inspect(context.Background(), Env{CallID: "c1", Cwd: w.root}, args, PolicyContext{Mode: ModeAsk}); codeOf(err) != CodeOutsideWorkspace {
		t.Fatalf("inspect code = %q (%v)", codeOf(err), err)
	}
	if _, err := st.Run(context.Background(), Env{CallID: "c1", Cwd: w.root}, args); codeOf(err) != CodeOutsideWorkspace {
		t.Fatalf("run code = %q (%v)", codeOf(err), err)
	}
	if runner.calls != 0 {
		t.Fatal("child started outside the workspace")
	}
}

// An omitted stdin is a nil slice, never an empty non-nil one
// (REVIEW-F1-SEC7 TL7-10).
func TestShellEmptyStdinIsNil(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	runner := &fakeRunner{}
	sh := fakeShell(t, w, runner, nil, nil)
	if _, err := sh.Run(context.Background(), Env{CallID: "c1", Cwd: w.root, InlineLeft: TurnInlineChars}, json.RawMessage(`{"command":"echo hi"}`)); err != nil {
		t.Fatal(err)
	}
	if runner.spec.Stdin != nil {
		t.Fatalf("stdin = %q, want nil", runner.spec.Stdin)
	}
}

// A spent inline budget stays spent: no call gets the full turn budget back
// (REVIEW-F1-SEC7 TL7-8).
func TestShellInlineBudgetStaysSpent(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	runner := &fakeRunner{}
	sh := fakeShell(t, w, runner, nil, nil)
	st, ok := sh.(*shellTool)
	if !ok {
		t.Fatal("shell tool has the wrong concrete type")
	}
	code := 0
	res := &exec.Result{Status: exec.StatusExited, ExitCode: &code, Output: []byte(strings.Repeat("x", 20000))}
	out, err := st.render(context.Background(), Env{InlineLeft: 0}, res, nil, classify.Verdict{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Content) >= len(res.Output) {
		t.Fatalf("spent budget inlined %d of %d bytes", len(out.Content), len(res.Output))
	}
	if out.Meta["log_blob_sha"] == nil {
		t.Fatalf("oversized output was not spilled: %+v", out.Meta)
	}
}

// The shell forwards the exec env policy, so a secret scoped to the command's
// binary reaches the child (ADR-088 butir 4; REVIEW-F1-SEC7 TL7-6 — exec
// exposes this as EnvPolicy.Secrets, not OneShot.SecretRefs).
func TestShellInjectsScopedSecret(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	id := classify.BinaryID{Path: "/bin/echo", Dev: 1, Ino: 2, Size: 3, MtimeNs: 4}
	runner := &fakeRunner{}
	sh := NewShell(&ShellConfig{
		Runner: runner, Spill: newMemSpill(), Redactor: redact.New(redact.Options{}),
		Roots: []string{w.root}, Home: w.root, Cwd: w.root,
		ParentEnv: os.Environ(),
		EnvPolicy: &exec.EnvPolicy{Secrets: []exec.SecretEnv{{
			Name: "DAMNBOT_TEST_SECRET", Value: "s3cr3t", Commands: []string{"echo"},
		}}},
		Allow:            []classify.AllowEntry{{"echo", "hi"}},
		SessionStart:     map[string]classify.BinaryID{"echo": id},
		Resolve:          func(string) (classify.BinaryID, bool) { return id, true },
		WorkspaceTrusted: true,
	})
	in, ok := sh.(Inspector)
	if !ok {
		t.Fatal("shell tool does not implement Inspector")
	}
	env := Env{CallID: "c1", Cwd: w.root, InlineLeft: TurnInlineChars}
	args := json.RawMessage(`{"command":"echo hi"}`)
	if _, err := in.Inspect(context.Background(), env, args, PolicyContext{Mode: ModeHeadless, WorkspaceTrusted: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := sh.Run(context.Background(), env, args); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(runner.spec.Env, "DAMNBOT_TEST_SECRET=s3cr3t") {
		t.Fatalf("secret not injected: %v", runner.spec.Env)
	}
}

// Effect classifies with the call's cwd, never the default one, so a command
// approved as destructive can never be retried as idempotent
// (REVIEW-F1-SEC7 TL7-7).
func TestShellEffectUsesCallCwd(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	sh := fakeShell(t, w, &fakeRunner{}, nil, nil)
	inside := sh.Effect(json.RawMessage(`{"command":"echo hi > out.txt"}`))
	out := json.RawMessage(`{"command":"echo hi > out.txt","cwd":` + strconv.Quote(filepath.Dir(w.root)) + `}`)
	if outside := sh.Effect(out); outside == EffectIdempotent {
		t.Fatalf("cwd outside the workspace classified idempotent (inside=%s)", inside)
	}
	if inside != EffectIdempotent {
		t.Fatalf("inside write = %s, want idempotent", inside)
	}
}
