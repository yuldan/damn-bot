package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/exec"
)

type runnerStub struct {
	res   exec.Result
	err   error
	calls int
}

func (r *runnerStub) Run(context.Context, *exec.Spec) (exec.Result, error) {
	r.calls++
	return r.res, r.err
}

func TestShellTerminalUsedWhenSupported(t *testing.T) {
	root := t.TempDir()
	zero := 0
	local := &runnerStub{res: exec.Result{Status: exec.StatusExited, ExitCode: &zero, Output: []byte("local")}}
	term := &runnerStub{res: exec.Result{Status: exec.StatusExited, ExitCode: &zero, Output: []byte("client-terminal")}}
	sh := mustShell(t, &ShellConfig{Roots: []string{root}, Cwd: root, Runner: local, Terminal: term, EnvPolicy: &exec.EnvPolicy{}, ParentEnv: []string{"PATH=" + os.Getenv("PATH")}})
	res, err := sh.Run(t.Context(), Env{}, json.RawMessage(`{"command":"echo hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "client-terminal") || local.calls != 0 || term.calls != 1 {
		t.Fatalf("content=%q local=%d term=%d", res.Content, local.calls, term.calls)
	}
}

func TestShellTerminalFallbackRecords(t *testing.T) {
	root := t.TempDir()
	zero := 0
	local := &runnerStub{res: exec.Result{Status: exec.StatusExited, ExitCode: &zero, Output: []byte("local-runner")}}
	term := &runnerStub{err: ErrTerminalUnsupported}
	fallbacks := 0
	sh := mustShell(t, &ShellConfig{
		Roots: []string{root}, Cwd: root, Runner: local, Terminal: term, EnvPolicy: &exec.EnvPolicy{}, ParentEnv: []string{"PATH=" + os.Getenv("PATH")},
		OnTerminalFallback: func(context.Context) error { fallbacks++; return nil },
	})
	res, err := sh.Run(t.Context(), Env{}, json.RawMessage(`{"command":"echo hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "local-runner") || local.calls != 1 || term.calls != 1 || fallbacks != 1 {
		t.Fatalf("content=%q local=%d term=%d fallbacks=%d", res.Content, local.calls, term.calls, fallbacks)
	}
}

func TestShellTerminalErrorDoesNotFallBack(t *testing.T) {
	root := t.TempDir()
	local := &runnerStub{res: exec.Result{Status: exec.StatusExited}}
	term := &runnerStub{err: errors.New("client terminal broke")}
	sh := mustShell(t, &ShellConfig{Roots: []string{root}, Cwd: root, Runner: local, Terminal: term, EnvPolicy: &exec.EnvPolicy{}, ParentEnv: []string{"PATH=" + os.Getenv("PATH")}})
	if _, err := sh.Run(t.Context(), Env{}, json.RawMessage(`{"command":"echo hi"}`)); err == nil || !strings.Contains(err.Error(), "client terminal broke") {
		t.Fatalf("err = %v", err)
	}
	if local.calls != 0 {
		t.Fatal("local runner ran after a client terminal failure")
	}
}
