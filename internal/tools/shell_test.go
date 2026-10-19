package tools

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/exec/classify"
	"github.com/damnbot/damnbot/internal/redact"
)

func testShell(t *testing.T, w *workspace, mode Mode) (Tool, func()) {
	return testShellTrusted(t, w, true)
}

func testShellTrusted(t *testing.T, w *workspace, trusted bool) (Tool, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	runner := exec.New(ctx)
	sh := NewShell(ShellConfig{
		Runner: runner, Spill: newMemSpill(), Redactor: redact.New(redact.Options{}),
		Roots: []string{w.root}, Home: w.root, Cwd: w.root,
		ParentEnv: os.Environ(), EnvPolicy: &exec.EnvPolicy{},
		WorkspaceTrusted: trusted,
	})
	return sh, func() {
		if err := runner.Close(); err != nil {
			t.Error(err)
		}
		cancel()
	}
}

func TestApprovalModesShell(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	sh, closeFn := testShell(t, w, ModeAsk)
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
	sh, closeFn := testShell(t, w, ModeHeadless)
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := newTestRunner(ctx)
	defer func() {
		if err := runner.Close(); err != nil {
			t.Error(err)
		}
	}()
	start := map[string]classify.BinaryID{"echo": {Path: "/bin/echo", Dev: 1, Ino: 2, Size: 3, MtimeNs: 4}}
	sh := NewShell(ShellConfig{
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
	sh, closeFn := testShell(t, w, ModeAuto)
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
	sh, closeFn := testShell(t, w, ModeAuto)
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
	sh, closeFn := testShell(t, w, ModeAuto)
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
	d, err := NewDispatcher(Config{
		Registry: reg, Journal: &journalRec{}, Spill: newMemSpill(), Mode: ModeAsk,
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
