package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/acp"
	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/tools"
)

// scriptedTerminal plays terminal/* for one runner.
type scriptedTerminal struct {
	mu       sync.Mutex
	outputs  []string
	exits    []*acp.TerminalExit
	waitExit acp.TerminalExit
	create   map[string]any
	calls    []string
	killed   bool
	released bool
	idx      int
}

func (s *scriptedTerminal) Request(_ context.Context, method string, params, result any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, method)
	switch method {
	case "terminal/create":
		if p, ok := params.(map[string]any); ok {
			s.create = p
		}
		return wrapJSON(json.Unmarshal([]byte(`{"terminalId":"t1"}`), result))
	case "terminal/output":
		body := ""
		if len(s.outputs) > 0 {
			body = s.outputs[min(s.idx, len(s.outputs)-1)]
		}
		var exit *acp.TerminalExit
		if len(s.exits) > 0 {
			exit = s.exits[min(s.idx, len(s.exits)-1)]
		}
		s.idx++
		b, merr := json.Marshal(map[string]any{"output": body, "truncated": false, "exitStatus": exit})
		if merr != nil {
			return fmt.Errorf("terminal fixture: %w", merr)
		}
		return wrapJSON(json.Unmarshal(b, result))
	case "terminal/wait_for_exit":
		b, merr := json.Marshal(s.waitExit)
		if merr != nil {
			return fmt.Errorf("terminal fixture: %w", merr)
		}
		return wrapJSON(json.Unmarshal(b, result))
	case "terminal/kill":
		s.killed = true
	case "terminal/release":
		s.released = true
	}
	return nil
}

func wrapJSON(err error) error {
	if err != nil {
		return fmt.Errorf("terminal fixture: %w", err)
	}
	return nil
}

func terminalRunner(t *testing.T, s *scriptedTerminal) *acpTerminalRunner {
	t.Helper()
	return &acpTerminalRunner{
		term: &acp.Terminal{SessionID: "s1", Enabled: true, Requester: s, Timeout: time.Second},
		red:  redact.New(redact.Options{}),
	}
}

func TestACPTerminalRunnerOutputAndEnv(t *testing.T) {
	zero := 0
	s := &scriptedTerminal{
		outputs:  []string{"hello client\n"},
		exits:    []*acp.TerminalExit{nil, {ExitCode: &zero}},
		waitExit: acp.TerminalExit{ExitCode: &zero},
	}
	secret := "sk-abcdefghijklmnopqrstuvwxyz012345"
	r := terminalRunner(t, s)
	res, err := r.Run(t.Context(), &exec.Spec{
		Argv: []string{"/bin/echo", "hi"}, Dir: "/work", Timeout: 2 * time.Second,
		Env: []string{"SAFE=ok", "MY_SECRET=" + secret, "PLAIN=" + secret, "PATH=/bin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(res.Output), "hello client") || res.Status != exec.StatusExited || res.Runner != acpTerminalName {
		t.Fatalf("result = %+v", res)
	}
	assertTerminalEnv(t, s, secret)
	if !s.released || !slices.Contains(s.calls, "terminal/wait_for_exit") {
		t.Fatalf("calls = %v released=%v", s.calls, s.released)
	}
	if strings.Contains(string(res.Output), secret) {
		t.Fatal("seed redaction failed")
	}
}

func assertTerminalEnv(t *testing.T, s *scriptedTerminal, secret string) {
	t.Helper()
	env, ok := s.create["env"].([]acp.TerminalEnv)
	if !ok {
		t.Fatalf("create env = %T", s.create["env"])
	}
	names := map[string]bool{}
	for _, e := range env {
		names[e.Name] = true
		if strings.Contains(e.Value, secret) {
			t.Fatalf("secret env leaked: %+v", e)
		}
	}
	if !names["SAFE"] || names["MY_SECRET"] || names["PLAIN"] {
		t.Fatalf("env = %+v", env)
	}
}

func TestACPTerminalRunnerRedactsOutput(t *testing.T) {
	zero := 0
	secret := "sk-abcdefghijklmnopqrstuvwxyz012345"
	s := &scriptedTerminal{outputs: []string{"token " + secret}, exits: []*acp.TerminalExit{{ExitCode: &zero}}, waitExit: acp.TerminalExit{ExitCode: &zero}}
	res, err := terminalRunner(t, s).Run(t.Context(), &exec.Spec{Argv: []string{"/bin/true"}, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(res.Output), secret) || !strings.Contains(string(res.Output), "[REDACTED") {
		t.Fatalf("output not redacted: %q", res.Output)
	}
}

func TestACPTerminalRunnerTimeoutKills(t *testing.T) {
	s := &scriptedTerminal{outputs: []string{"waiting"}, exits: []*acp.TerminalExit{nil, nil, {Signal: "SIGKILL"}}, waitExit: acp.TerminalExit{Signal: "SIGKILL"}}
	res, err := terminalRunner(t, s).Run(t.Context(), &exec.Spec{Argv: []string{"/bin/sleep", "9"}, Timeout: 40 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != exec.StatusTimeout || !s.killed || !slices.Contains(s.calls, "terminal/kill") || !slices.Contains(s.calls, "terminal/wait_for_exit") {
		t.Fatalf("timeout result = %+v calls=%v killed=%v", res, s.calls, s.killed)
	}
}

func TestACPTerminalRunnerOutputCap(t *testing.T) {
	s := &scriptedTerminal{outputs: []string{strings.Repeat("a", acpTerminalCap+16)}, exits: []*acp.TerminalExit{nil, {Signal: "SIGKILL"}}, waitExit: acp.TerminalExit{Signal: "SIGKILL"}}
	res, err := terminalRunner(t, s).Run(t.Context(), &exec.Spec{Argv: []string{"/bin/yes"}, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated || !strings.Contains(string(res.Output), "[truncated:") || len(res.Output) > acpTerminalCap+len(acpTerminalMarker)+1 {
		t.Fatalf("cap result: len=%d truncated=%v", len(res.Output), res.Truncated)
	}
	if !s.killed {
		t.Fatal("capped terminal was not killed")
	}
}

func TestACPTerminalRunnerUnsupported(t *testing.T) {
	r := &acpTerminalRunner{term: &acp.Terminal{Enabled: false, Requester: &scriptedTerminal{}}, red: redact.New(redact.Options{})}
	if _, err := r.Run(t.Context(), &exec.Spec{Argv: []string{"/bin/true"}}); !errors.Is(err, tools.ErrTerminalUnsupported) {
		t.Fatalf("unsupported = %v", err)
	}
}
