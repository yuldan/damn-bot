package acp

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Terminal is the agent→client terminal adapter (SPEC §13.3, GAP-UX-01
// butir b): the agent creates a process on the client, polls its output,
// waits for exit, kills and releases it. It never executes anything itself.
// A missing capability is reported as unsupported so the caller falls back
// to the local runner and records one event.
type Terminal struct {
	SessionID string
	Enabled   bool
	Requester Requester
	Timeout   time.Duration
}

// Supports reports whether the client advertised terminal/*.
func (t *Terminal) Supports() bool {
	return t != nil && t.Enabled && t.Requester != nil
}

// TerminalEnv is one environment entry sent to terminal/create.
type TerminalEnv struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// TerminalExit is the exit state reported by output/wait_for_exit.
type TerminalExit struct {
	ExitCode *int   `json:"exitCode,omitempty"`
	Signal   string `json:"signal,omitempty"`
}

// Create starts one process on the client and returns its id.
func (t *Terminal) Create(ctx context.Context, command string, args []string, cwd string, env []TerminalEnv, outputLimit int) (string, error) {
	var res struct {
		TerminalID string `json:"terminalId"`
	}
	params := map[string]any{"sessionId": t.SessionID, "command": command}
	if len(args) > 0 {
		params["args"] = args
	}
	if cwd != "" {
		params["cwd"] = cwd
	}
	if len(env) > 0 {
		params["env"] = env
	}
	if outputLimit > 0 {
		params["outputByteLimit"] = outputLimit
	}
	if err := t.call(ctx, "terminal/create", params, &res); err != nil {
		return "", err
	}
	if res.TerminalID == "" {
		return "", errors.New("acp client: terminal/create returned no terminalId")
	}
	return res.TerminalID, nil
}

// Output polls the client's buffer for terminalID.
func (t *Terminal) Output(ctx context.Context, terminalID string) ([]byte, bool, *TerminalExit, error) {
	var res struct {
		Output     string        `json:"output"`
		Truncated  bool          `json:"truncated"`
		ExitStatus *TerminalExit `json:"exitStatus"`
	}
	if err := t.call(ctx, "terminal/output", map[string]any{"sessionId": t.SessionID, "terminalId": terminalID}, &res); err != nil {
		return nil, false, nil, err
	}
	return []byte(res.Output), res.Truncated, res.ExitStatus, nil
}

// WaitForExit blocks until the client reports the exit state.
func (t *Terminal) WaitForExit(ctx context.Context, terminalID string) (TerminalExit, error) {
	var res TerminalExit
	if err := t.call(ctx, "terminal/wait_for_exit", map[string]any{"sessionId": t.SessionID, "terminalId": terminalID}, &res); err != nil {
		return TerminalExit{}, err
	}
	return res, nil
}

// Kill asks the client to terminate the process.
func (t *Terminal) Kill(ctx context.Context, terminalID string) error {
	return t.call(ctx, "terminal/kill", map[string]any{"sessionId": t.SessionID, "terminalId": terminalID}, nil)
}

// Release frees the client's terminal resources.
func (t *Terminal) Release(ctx context.Context, terminalID string) error {
	return t.call(ctx, "terminal/release", map[string]any{"sessionId": t.SessionID, "terminalId": terminalID}, nil)
}

func (t *Terminal) call(ctx context.Context, method string, params, result any) error {
	if !t.Supports() {
		return ErrTerminalUnsupported
	}
	wait := t.Timeout
	if wait <= 0 {
		wait = DefaultClientFSTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	if err := t.Requester.Request(ctx, method, params, result); err != nil {
		return fmt.Errorf("acp client %s: %w", method, err)
	}
	return nil
}

// ErrTerminalUnsupported reports that the client did not advertise
// terminal/* (or the option is off); the shell tool then falls back locally.
var ErrTerminalUnsupported = errors.New("acp client: terminal capability unavailable")
