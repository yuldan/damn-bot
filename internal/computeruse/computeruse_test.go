package computeruse

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
)

func TestActionValidate(t *testing.T) {
	t.Parallel()
	valid := []Action{
		{Kind: ActionMove, X: 0, Y: 0},
		{Kind: ActionClick, X: 10, Y: 20, Button: "right"},
		{Kind: ActionType, Text: "halo"},
		{Kind: ActionKey, Key: "ctrl+c"},
		{Kind: ActionKey, Key: "f12"},
		{Kind: ActionScroll, DY: -3},
		{Kind: ActionWait, WaitMS: 100},
		{Kind: ActionScreenshot},
	}
	for _, a := range valid {
		if err := a.Validate(); err != nil {
			t.Errorf("%+v: %v", a, err)
		}
	}
	invalid := []Action{
		{Kind: "drag"},
		{Kind: ActionClick, X: -1},
		{Kind: ActionClick, X: maxCoordinate + 1},
		{Kind: ActionClick, Button: "back"},
		{Kind: ActionType},
		{Kind: ActionType, Text: "a\x00b"},
		{Kind: ActionType, Text: strings.Repeat("x", MaxTextBytes+1)},
		{Kind: ActionKey, Key: "ctrl+"},
		{Kind: ActionKey, Key: "hyper+a"},
		{Kind: ActionKey, Key: "f25"},
		{Kind: ActionScroll},
		{Kind: ActionScroll, DY: maxScroll + 1},
		{Kind: ActionWait},
		{Kind: ActionWait, WaitMS: maxWaitMS + 1},
	}
	for _, a := range invalid {
		if err := a.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: err = %v, want ErrInvalid", a, err)
		}
	}
}

// fakeApprovals records requests and returns the configured answer.
type fakeApprovals struct {
	answer host.Approval
	err    error
	mu     sync.Mutex
	reqs   []host.ApprovalRequest
}

func (f *fakeApprovals) Approve(_ context.Context, req *host.ApprovalRequest) (host.Approval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, *req)
	return f.answer, f.err
}

func (f *fakeApprovals) requests() []host.ApprovalRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]host.ApprovalRequest(nil), f.reqs...)
}

type fakeSecrets struct {
	mu     sync.Mutex
	policy host.AllowPolicy
	values map[string]string
	err    error
}

func (f *fakeSecrets) ChildEnv(policy host.AllowPolicy) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.policy = policy
	if f.err != nil {
		return nil, f.err
	}
	var out []string
	for _, name := range policy.Inherit {
		out = append(out, name+"="+f.values[name])
	}
	return out, nil
}

// fakeBackend implements Backend for module-level policy tests.
type fakeBackend struct {
	mu      sync.Mutex
	calls   []Action
	output  string
	png     []byte
	started int
	stopped int
	err     error
	block   bool
}

func (f *fakeBackend) Start(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started++
	return f.err
}

func (f *fakeBackend) Act(ctx context.Context, a *Action) (Result, error) {
	if f.block {
		<-ctx.Done()
		return Result{}, fmt.Errorf("blocked: %w", ctx.Err())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, *a)
	if f.err != nil {
		return Result{}, f.err
	}
	if a.Kind == ActionScreenshot {
		return Result{Image: append([]byte(nil), f.png...)}, nil
	}
	return Result{Output: f.output}, nil
}

func (f *fakeBackend) Screenshot(ctx context.Context) ([]byte, error) {
	if f.block {
		<-ctx.Done()
		return nil, fmt.Errorf("blocked: %w", ctx.Err())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Action{Kind: ActionScreenshot})
	if f.err != nil {
		return nil, f.err
	}
	return append([]byte(nil), f.png...), nil
}

func (f *fakeBackend) Stop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopped++
	return f.err
}

func (f *fakeBackend) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// testModule wires a fake backend without going through New (which only builds
// the two real transports).
func testModule(t *testing.T, b Backend, approvals host.Approvals, max int) *Module {
	t.Helper()
	return &Module{
		cfg:     Config{Timeout: time.Second, MaxScreenshotBytes: max},
		deps:    Deps{Approvals: approvals, Secrets: host.DenySecrets{}},
		backend: b,
	}
}

func TestApprovalEveryInput(t *testing.T) {
	t.Parallel()
	for _, a := range []Action{
		{Kind: ActionClick, X: 4, Y: 5},
		{Kind: ActionType, Text: "hunter2"},
		{Kind: ActionKey, Key: "alt+tab"},
		{Kind: ActionScroll, DY: 2},
	} {
		t.Run(string(a.Kind), func(t *testing.T) {
			t.Parallel()
			assertDeniedApproval(t, &a)
		})
	}
}

func assertDeniedApproval(t *testing.T, a *Action) {
	t.Helper()
	backend := &fakeBackend{output: "ok"}
	appr := &fakeApprovals{answer: host.ApprovalDeny}
	m := testModule(t, backend, appr, 1024)
	if _, err := m.Act(context.Background(), "call-1", a); !errors.Is(err, ErrDenied) {
		t.Fatalf("Act: %v, want ErrDenied", err)
	}
	if backend.count() != 0 {
		t.Fatalf("denied action reached the backend %d times", backend.count())
	}
	reqs := appr.requests()
	if len(reqs) != 1 {
		t.Fatalf("requests = %d, want 1", len(reqs))
	}
	req := reqs[0]
	if req.Effect != host.EffectNonIdempotent || req.Class != "computer_use:"+string(a.Kind) {
		t.Fatalf("request = %+v", req)
	}
	if err := req.Validate(); err != nil {
		t.Fatalf("request does not validate: %v", err)
	}
	if len([]rune(req.Preview)) > host.MaxApprovalPreview {
		t.Fatalf("preview longer than the cap: %q", req.Preview)
	}
	if a.Kind == ActionType && (strings.Contains(req.Preview, a.Text) || bytes.Contains(req.Args, []byte(a.Text))) {
		t.Fatalf("typed text leaked into preview/args: %q %s", req.Preview, req.Args)
	}
}

func TestApprovalAllowSessionDenied(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{output: "ok"}
	appr := &fakeApprovals{answer: host.ApprovalAllowSession}
	m := testModule(t, backend, appr, 1024)
	if _, err := m.Act(context.Background(), "c", &Action{Kind: ActionClick}); !errors.Is(err, ErrDenied) {
		t.Fatalf("Act: %v, want ErrDenied for allow_session", err)
	}
	if backend.count() != 0 {
		t.Fatal("allow_session reached the backend")
	}
}

func TestReadActionsSkipApproval(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{output: "ok", png: []byte("png")}
	appr := &fakeApprovals{answer: host.ApprovalDeny}
	m := testModule(t, backend, appr, 1024)
	for _, a := range []Action{
		{Kind: ActionMove, X: 1, Y: 2},
		{Kind: ActionWait, WaitMS: 5},
	} {
		if _, err := m.Act(context.Background(), "c", &a); err != nil {
			t.Fatalf("%s: %v", a.Kind, err)
		}
	}
	res, err := m.Act(context.Background(), "c", &Action{Kind: ActionScreenshot})
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if res.UntrustedSource != "computer_use:screenshot" {
		t.Fatalf("untrusted source = %q", res.UntrustedSource)
	}
	if len(appr.requests()) != 0 {
		t.Fatalf("read actions asked for approval: %+v", appr.requests())
	}
}

func TestScreenshotOversize(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{png: bytes.Repeat([]byte("x"), 9)}
	m := testModule(t, backend, &fakeApprovals{answer: host.ApprovalAllowOnce}, 8)
	if _, err := m.Screenshot(context.Background()); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Screenshot: %v, want ErrTooLarge", err)
	}
	_, err := m.Invoke(context.Background(), ToolName, json.RawMessage(`{"kind":"screenshot"}`), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "too_large" {
		t.Fatalf("Invoke: %v, want too_large", err)
	}
}

func TestActionTimeout(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{block: true}
	m := &Module{
		cfg:     Config{Timeout: 20 * time.Millisecond, MaxScreenshotBytes: 1024},
		deps:    Deps{Approvals: &fakeApprovals{answer: host.ApprovalAllowOnce}},
		backend: backend,
	}
	_, err := m.Act(context.Background(), "c", &Action{Kind: ActionClick})
	if !errors.Is(err, ErrTimeout) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Act: %v, want timeout", err)
	}
	_, err = m.Invoke(context.Background(), ToolName, json.RawMessage(`{"kind":"click"}`), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "timeout" {
		t.Fatalf("Invoke: %v, want timeout", err)
	}
}

func TestUnavailable(t *testing.T) {
	t.Parallel()
	for _, cfg := range []Config{
		{},
		{Command: []string{filepath.Join(t.TempDir(), "absent")}},
	} {
		m, err := New(cfg, Deps{})
		if err != nil {
			t.Fatalf("New(%+v): %v", cfg, err)
		}
		if serr := m.Start(context.Background()); !errors.Is(serr, ErrUnavailable) {
			t.Errorf("Start(%+v): %v, want ErrUnavailable", cfg, serr)
		}
		_, err = m.Invoke(context.Background(), ToolName, json.RawMessage(`{"kind":"click"}`), host.Env{})
		var he *host.Error
		if !errors.As(err, &he) || he.Code != "module_unavailable" {
			t.Errorf("Invoke(%+v): %v, want module_unavailable", cfg, err)
		}
	}
}

func TestInvokeRejectsInvalid(t *testing.T) {
	t.Parallel()
	m := testModule(t, &fakeBackend{output: "ok"}, &fakeApprovals{answer: host.ApprovalAllowOnce}, 1024)
	for _, tc := range []struct {
		tool string
		args string
	}{
		{"other", `{"kind":"click"}`},
		{ToolName, `{"kind":"drag"}`},
		{ToolName, `{"kind":"click","bogus":1}`},
		{ToolName, `not json`},
		{ToolName, `{"kind":"type","text":""}`},
	} {
		_, err := m.Invoke(context.Background(), tc.tool, json.RawMessage(tc.args), host.Env{})
		var he *host.Error
		if !errors.As(err, &he) || (he.Code != "invalid_args" && he.Code != "unknown_tool") {
			t.Errorf("Invoke(%s,%s) = %v", tc.tool, tc.args, err)
		}
	}
}

// staticProcess is a host.Process whose pipes are already complete.
type staticProcess struct {
	in     bytes.Buffer
	stdout io.Reader
	stderr io.Reader
	done   chan struct{}
}

func newStaticProcess(stdout []byte) *staticProcess {
	p := &staticProcess{stdout: bytes.NewReader(stdout), stderr: bytes.NewReader(nil), done: make(chan struct{})}
	close(p.done)
	return p
}

func (p *staticProcess) Stdio() (io.WriteCloser, io.Reader, io.Reader) {
	return closeNop{&p.in}, p.stdout, p.stderr
}
func (p *staticProcess) Done() <-chan struct{} { return p.done }
func (p *staticProcess) Wait() error           { return nil }
func (p *staticProcess) Signal(os.Signal) error {
	return nil
}
func (p *staticProcess) Kill() error { return nil }

type closeNop struct{ w io.Writer }

func (c closeNop) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	if err != nil {
		return n, fmt.Errorf("write: %w", err)
	}
	return n, nil
}
func (closeNop) Close() error { return nil }

func TestProcessBackendArgvAndStdin(t *testing.T) {
	t.Parallel()
	exe := filepath.Join(t.TempDir(), "cua")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var (
		mu    sync.Mutex
		specs []host.SpawnSpec
		procs []*staticProcess
	)
	spawn := func(_ context.Context, spec host.SpawnSpec) (host.Process, error) {
		mu.Lock()
		defer mu.Unlock()
		specs = append(specs, spec)
		p := newStaticProcess([]byte("ok\n"))
		procs = append(procs, p)
		return p, nil
	}
	secrets := &fakeSecrets{values: map[string]string{"DISPLAY": ":0"}}
	m, err := New(Config{
		Command: []string{exe, "--driver"}, Env: []string{"DISPLAY"},
		Timeout: time.Second, MaxScreenshotBytes: 1024,
	}, Deps{Spawn: spawn, Secrets: secrets, Approvals: &fakeApprovals{answer: host.ApprovalAllowOnce}})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := m.Act(context.Background(), "c", &Action{Kind: ActionClick, X: 10, Y: 20}); err != nil {
		t.Fatalf("click: %v", err)
	}
	if _, err := m.Act(context.Background(), "c", &Action{Kind: ActionType, Text: "hunter2"}); err != nil {
		t.Fatalf("type: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(specs) != 2 {
		t.Fatalf("spawns = %d, want 2", len(specs))
	}
	if strings.Join(specs[0].Args, " ") != "--driver click --x 10 --y 20 --button left" {
		t.Fatalf("click argv = %q", specs[0].Args)
	}
	for _, arg := range specs[1].Args {
		if strings.Contains(arg, "hunter2") {
			t.Fatalf("typed text leaked into argv: %q", specs[1].Args)
		}
	}
	if strings.Join(specs[1].Args, " ") != "--driver type" {
		t.Fatalf("type argv = %q", specs[1].Args)
	}
	if got := procs[1].in.String(); got != "hunter2" {
		t.Fatalf("stdin = %q, want typed text", got)
	}
	if got := strings.Join(specs[0].Env, ","); got != "DISPLAY=:0" {
		t.Fatalf("env = %q", got)
	}
	if len(secrets.policy.Inherit) != 1 || secrets.policy.Inherit[0] != "DISPLAY" {
		t.Fatalf("env policy = %+v", secrets.policy)
	}
}

func TestProcessBackendScreenshot(t *testing.T) {
	t.Parallel()
	exe := filepath.Join(t.TempDir(), "cua")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	png := []byte("\x89PNG\r\n\x1a\npayload")
	spawn := func(_ context.Context, _ host.SpawnSpec) (host.Process, error) {
		return newStaticProcess(png), nil
	}
	m, err := New(Config{Command: []string{exe}, Timeout: time.Second, MaxScreenshotBytes: 1024},
		Deps{Spawn: spawn, Secrets: &fakeSecrets{}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Screenshot(context.Background())
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if !bytes.Equal(got, png) {
		t.Fatalf("png = %q", got)
	}
}

// TestF6TOOL023ComputerUse is the acceptance row test: every input action asks
// for approval, a denial executes zero actions, and a screenshot is read-only
// but tainted.
func TestF6TOOL023ComputerUse(t *testing.T) {
	t.Parallel()
	for _, a := range []Action{
		{Kind: ActionClick},
		{Kind: ActionType, Text: "secret"},
		{Kind: ActionKey, Key: "enter"},
		{Kind: ActionScroll, DY: 1},
	} {
		backend := &fakeBackend{}
		appr := &fakeApprovals{answer: host.ApprovalDeny}
		m := testModule(t, backend, appr, 1024)
		if _, err := m.Act(context.Background(), "c", &a); !errors.Is(err, ErrDenied) {
			t.Fatalf("%s: %v", a.Kind, err)
		}
		if backend.count() != 0 {
			t.Fatalf("%s executed after denial", a.Kind)
		}
	}
	backend := &fakeBackend{png: []byte("png")}
	m := testModule(t, backend, &fakeApprovals{answer: host.ApprovalDeny}, 1024)
	res, err := m.Invoke(context.Background(), ToolName, json.RawMessage(`{"kind":"screenshot"}`), host.Env{})
	if err != nil || res.UntrustedSource != "computer_use:screenshot" {
		t.Fatalf("screenshot = %+v, %v", res, err)
	}
}

func TestInvalidConfig(t *testing.T) {
	t.Parallel()
	for _, cfg := range []Config{
		{Command: []string{"cua"}},
		{Command: []string{"/usr/bin/cua", "a\x00b"}},
		{Env: []string{"BAD-NAME"}},
		{Timeout: -1},
		{MaxScreenshotBytes: -1},
	} {
		if _, err := New(cfg, Deps{}); !errors.Is(err, ErrInvalid) {
			t.Errorf("New(%+v) = %v, want ErrInvalid", cfg, err)
		}
	}
}
