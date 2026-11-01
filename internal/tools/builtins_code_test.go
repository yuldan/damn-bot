//go:build !minimal

package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/exec/code"
)

type fakeCodeRunner struct {
	calls int
	res   code.Result
	err   error
}

func (f *fakeCodeRunner) Run(context.Context, *code.Request) (code.Result, error) {
	f.calls++
	return f.res, f.err
}

func TestExecuteCodeApproval(t *testing.T) {
	t.Parallel()
	r := &fakeCodeRunner{res: code.Result{Status: exec.StatusExited, ExitCode: new(0), Stdout: []byte("ok")}}
	base := NewExecuteCode(&ExecuteCodeConfig{Runner: r, AutoLanguages: []string{"python"}})
	tool, ok := base.(Inspector)
	if !ok {
		t.Fatal("tool is not an Inspector")
	}
	args := func(lang string) json.RawMessage {
		return json.RawMessage(`{"language":"` + lang + `","code":"print(1)"}`)
	}
	cases := []struct {
		name string
		pc   PolicyContext
		lang string
		want Requirement
	}{
		{"ask always asks", PolicyContext{Mode: ModeAsk, WorkspaceTrusted: true}, "python", RequireAsk},
		{"auto-edit asks", PolicyContext{Mode: ModeAutoEdit, WorkspaceTrusted: true}, "python", RequireAsk},
		{"auto allowed trusted", PolicyContext{Mode: ModeAuto, WorkspaceTrusted: true}, "python", RequireAuto},
		{"auto not allowed", PolicyContext{Mode: ModeAuto, WorkspaceTrusted: true}, "go", RequireAsk},
		{"auto untrusted", PolicyContext{Mode: ModeAuto}, "python", RequireAsk},
		{"auto tainted", PolicyContext{Mode: ModeAuto, WorkspaceTrusted: true, Tainted: true}, "python", RequireAsk},
		{"headless auto allowed", PolicyContext{Mode: ModeHeadlessAuto, WorkspaceTrusted: true}, "python", RequireAuto},
		{"headless ask denies", PolicyContext{Mode: ModeHeadless, WorkspaceTrusted: true}, "python", RequireDeny},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := tool.Inspect(t.Context(), Env{}, args(tc.lang), tc.pc)
			if err != nil || d.Requirement != tc.want {
				t.Fatalf("Inspect = %+v, %v; want %v", d, err, tc.want)
			}
			if d.AllowSession {
				t.Fatal("execute_code must never allow session approval")
			}
			if d.Requirement == RequireDeny && r.calls != 0 {
				t.Fatalf("deny spawned %d", r.calls)
			}
		})
	}
	if d, ierr := tool.Inspect(t.Context(), Env{}, args("python"), PolicyContext{Mode: ModeAuto, WorkspaceTrusted: true}); ierr != nil || d.Preview == "" || containsCode(d.Preview) {
		t.Fatalf("preview leaks code or is empty: %q", d.Preview)
	}
	if _, err := tool.Inspect(t.Context(), Env{}, args("bash"), PolicyContext{Mode: ModeAuto}); err == nil {
		t.Fatal("shell language accepted")
	}
	if _, err := tool.Inspect(t.Context(), Env{}, json.RawMessage(`{"language":"rust","code":"x"}`), PolicyContext{Mode: ModeAuto}); err == nil {
		t.Fatal("unsupported language accepted")
	}
}

func TestExecuteCodeRun(t *testing.T) {
	t.Parallel()
	r := &fakeCodeRunner{res: code.Result{Status: exec.StatusExited, ExitCode: new(0), Stdout: []byte("hello"), Backend: "fake", Isolation: code.Isolation{Network: "deny", Memory: "none"}}}
	tool := NewExecuteCode(&ExecuteCodeConfig{Runner: r})
	res, err := tool.Run(t.Context(), Env{}, json.RawMessage(`{"language":"python","code":"print(1)","stdin":"{}"}`))
	if err != nil || !res.OK || res.Content != "hello" || r.calls != 1 {
		t.Fatalf("Run = %+v err=%v calls=%d", res, err, r.calls)
	}
	r.res.ExitCode = new(3)
	res, rerr := tool.Run(t.Context(), Env{}, json.RawMessage(`{"language":"python","code":"exit(3)"}`))
	if rerr != nil || res.OK {
		t.Fatalf("nonzero exit: rerr=%v OK=%v", rerr, res.OK)
	}
	r.err = code.ErrNetworkDeny
	if _, err := tool.Run(t.Context(), Env{}, json.RawMessage(`{"language":"python","code":"1"}`)); err == nil {
		t.Fatal("network deny not reported")
	}
	if _, err := tool.Run(t.Context(), Env{}, json.RawMessage(`{"language":"python"}`)); err == nil {
		t.Fatal("missing code accepted")
	}
}

func TestExecuteCodeRunnerMissing(t *testing.T) {
	t.Parallel()
	tool := NewExecuteCode(&ExecuteCodeConfig{})
	if _, err := tool.Run(t.Context(), Env{}, json.RawMessage(`{"language":"python","code":"x"}`)); err == nil {
		t.Fatal("nil runner accepted")
	}
}

func containsCode(preview string) bool { return previewContains(preview) }
func previewContains(p string) bool {
	for i := 0; i+6 <= len(p); i++ {
		if p[i:i+6] == "print(" {
			return true
		}
	}
	return false
}
