package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type terminalRequester struct {
	calls  []string
	create map[string]any
}

func (f *terminalRequester) Request(_ context.Context, method string, params, result any) error {
	f.calls = append(f.calls, method)
	p, ok := params.(map[string]any)
	if !ok {
		p = map[string]any{}
	}
	switch method {
	case "terminal/create":
		f.create = p
		return decodeFixture(`{"terminalId":"t1"}`, result)
	case "terminal/output":
		return decodeFixture(`{"output":"out","truncated":false,"exitStatus":{"exitCode":0}}`, result)
	case "terminal/wait_for_exit":
		return decodeFixture(`{"exitCode":0}`, result)
	}
	return nil
}

func decodeFixture(raw string, result any) error {
	if err := json.Unmarshal([]byte(raw), result); err != nil {
		return fmt.Errorf("terminal fixture: %w", err)
	}
	return nil
}

func TestTerminalCreate(t *testing.T) {
	r := &terminalRequester{}
	term := &Terminal{SessionID: "s1", Enabled: true, Requester: r, Timeout: time.Second}
	id, err := term.Create(t.Context(), "/bin/echo", []string{"hi"}, "/work", []TerminalEnv{{Name: "A", Value: "b"}}, 1024)
	if err != nil || id != "t1" {
		t.Fatalf("Create = %q, %v", id, err)
	}
	if r.create["sessionId"] != "s1" || r.create["command"] != "/bin/echo" || r.create["cwd"] != "/work" || r.create["outputByteLimit"] != 1024 {
		t.Fatalf("create params = %+v", r.create)
	}
}

func TestTerminalOutput(t *testing.T) {
	term := &Terminal{SessionID: "s1", Enabled: true, Requester: &terminalRequester{}, Timeout: time.Second}
	out, truncated, exit, err := term.Output(t.Context(), "t1")
	if err != nil || string(out) != "out" || truncated || exit == nil || exit.ExitCode == nil || *exit.ExitCode != 0 {
		t.Fatalf("Output = %q %v %+v %v", out, truncated, exit, err)
	}
	if _, err := term.WaitForExit(t.Context(), "t1"); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalKillRelease(t *testing.T) {
	r := &terminalRequester{}
	term := &Terminal{SessionID: "s1", Enabled: true, Requester: r, Timeout: time.Second}
	if _, err := term.Create(t.Context(), "/bin/echo", nil, "", nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := term.Kill(t.Context(), "t1"); err != nil {
		t.Fatal(err)
	}
	if err := term.Release(t.Context(), "t1"); err != nil {
		t.Fatal(err)
	}
	want := []string{"terminal/create", "terminal/kill", "terminal/release"}
	for i, m := range want {
		if r.calls[i] != m {
			t.Fatalf("calls = %v", r.calls)
		}
	}
}

func TestTerminalUnsupported(t *testing.T) {
	term := &Terminal{Enabled: false, Requester: &terminalRequester{}}
	if term.Supports() {
		t.Fatal("disabled terminal reported supported")
	}
	if _, err := term.Create(t.Context(), "x", nil, "", nil, 0); !errors.Is(err, ErrTerminalUnsupported) {
		t.Fatalf("Create = %v", err)
	}
}

func TestTerminalErrorWrapped(t *testing.T) {
	term := &Terminal{Enabled: true, Requester: requesterFunc(func(context.Context, string, any, any) error {
		return errors.New("client went away")
	})}
	if err := term.Kill(t.Context(), "t1"); err == nil || !strings.Contains(err.Error(), "client went away") {
		t.Fatalf("Kill = %v", err)
	}
}

type requesterFunc func(context.Context, string, any, any) error

func (f requesterFunc) Request(ctx context.Context, method string, params, result any) error {
	return f(ctx, method, params, result)
}
