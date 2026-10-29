package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// lockedBuf is a process output buffer readable while the child writes it.
type lockedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, err := b.buf.Write(p); err != nil {
		return 0, fmt.Errorf("buffer: %w", err)
	}
	return len(p), nil
}

func (b *lockedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// termProc is one process started by the fake client terminal.
type termProc struct {
	cmd    *exec.Cmd
	out    *lockedBuf
	done   chan struct{}
	mu     sync.Mutex
	exit   map[string]any
	killed bool
}

// fakeTerminals implements terminal/* by running real processes locally, as a
// client terminal would.
type fakeTerminals struct {
	t        *testing.T
	mu       sync.Mutex
	procs    map[string]*termProc
	seq      int
	creates  []map[string]any
	kills    int
	releases int
}

func (f *fakeTerminals) handle(method string, params map[string]any) (any, map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := frameString(params, "terminalId")
	switch method {
	case "terminal/create":
		return f.start(params)
	case "terminal/output":
		return f.poll(id)
	case "terminal/wait_for_exit":
		return f.wait(id)
	case "terminal/kill":
		f.stop(id)
		return map[string]any{}, nil
	case "terminal/release":
		f.release(id)
		return map[string]any{}, nil
	}
	return nil, map[string]any{"code": -32601, "message": "method not found"}
}

func (f *fakeTerminals) start(params map[string]any) (any, map[string]any) {
	f.creates = append(f.creates, params)
	proc := exec.CommandContext(context.Background(), frameString(params, "command"), stringsFrom(params["args"])...) // #nosec G204 -- fake client runs the requested command
	proc.Dir = frameString(params, "cwd")
	proc.Env = append(proc.Env, envPairs(params["env"])...)
	out := &lockedBuf{}
	proc.Stdout, proc.Stderr = out, out
	tp := &termProc{cmd: proc, out: out, done: make(chan struct{})}
	if err := proc.Start(); err != nil {
		return nil, map[string]any{"code": -32603, "message": "client terminal spawn: " + err.Error()}
	}
	f.seq++
	tid := fmt.Sprintf("term-%d", f.seq)
	f.procs[tid] = tp
	go f.reap(tp)
	return map[string]any{"terminalId": tid}, nil
}

// reap records the exit state once the process ends.
func (f *fakeTerminals) reap(tp *termProc) {
	err := tp.cmd.Wait()
	tp.mu.Lock()
	defer tp.mu.Unlock()
	switch {
	case tp.killed:
		tp.exit = map[string]any{"signal": "SIGKILL"}
	case err == nil:
		tp.exit = map[string]any{"exitCode": 0}
	default:
		code := 1
		if ee, ok := errors.AsType[*exec.ExitError](err); ok {
			code = ee.ExitCode()
		}
		tp.exit = map[string]any{"exitCode": code}
	}
	close(tp.done)
}

func (f *fakeTerminals) poll(id string) (any, map[string]any) {
	tp := f.procs[id]
	if tp == nil {
		return nil, map[string]any{"code": -32602, "message": "unknown terminal", "data": map[string]any{"error_code": "not_found"}}
	}
	select {
	case <-tp.done:
		tp.mu.Lock()
		defer tp.mu.Unlock()
		return map[string]any{"output": tp.out.String(), "truncated": false, "exitStatus": cloneMap(tp.exit)}, nil
	default:
		return map[string]any{"output": tp.out.String(), "truncated": false}, nil
	}
}

func (f *fakeTerminals) wait(id string) (any, map[string]any) {
	tp := f.procs[id]
	if tp == nil {
		return nil, map[string]any{"code": -32602, "message": "unknown terminal"}
	}
	<-tp.done
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return cloneMap(tp.exit), nil
}

func (f *fakeTerminals) stop(id string) {
	tp := f.procs[id]
	if tp == nil {
		return
	}
	f.kills++
	tp.mu.Lock()
	tp.killed = true
	tp.mu.Unlock()
	if err := tp.cmd.Process.Kill(); err != nil {
		return
	}
	<-tp.done
}

func (f *fakeTerminals) release(id string) {
	f.releases++
	tp := f.procs[id]
	if tp == nil {
		return
	}
	if err := tp.cmd.Process.Kill(); err != nil {
		return
	}
	delete(f.procs, id)
}

func (f *fakeTerminals) cleanup() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, tp := range f.procs {
		if err := tp.cmd.Process.Kill(); err != nil {
			continue
		}
		select {
		case <-tp.done:
		case <-time.After(2 * time.Second):
		}
	}
}

func stringsFrom(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func envPairs(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		name := frameString(m, "name")
		value := frameString(m, "value")
		if name != "" {
			out = append(out, name+"="+value)
		}
	}
	return out
}

func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	maps.Copy(out, m)
	return out
}

// startClientTerm opens a session advertising (or not) terminal/*.
func startClientTerm(t *testing.T, e *Env, cfg string, term bool, env ...string) *fsClient {
	t.Helper()
	p := startFSACP(t, e, cfg, env...)
	p.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"terminal": term,
		},
	}})
	if f := p.recv(); f["id"] != float64(1) || f["result"] == nil {
		t.Fatalf("initialize = %v", f)
	}
	p.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": map[string]any{"cwd": e.Workdir}})
	f := p.recv()
	res, ok := f["result"].(map[string]any)
	if !ok {
		t.Fatalf("session/new = %v", f)
	}
	sid, ok := res["sessionId"].(string)
	if !ok || sid == "" {
		t.Fatalf("session/new = %v", f)
	}
	p.sid = sid
	c := &fsClient{t: t, p: p, buffer: map[string]string{}, seen: map[string]string{}, terminal: &fakeTerminals{t: t, procs: map[string]*termProc{}}}
	t.Cleanup(c.terminal.cleanup)
	return c
}

const terminalConfig = acpFSConfig + `
[acp]
use_client_terminal = true
`

func TestACPClientTerminalRunsOnClient(t *testing.T) {
	e := newEnv(t)
	srv := providertest.New(t, nil,
		ToolCallReply("call-1", "shell", `{"command":"printf 'client-terminal-ok\\n'","timeout_s":10}`),
		providertest.Text("done"),
	)
	c := startClientTerm(t, e, terminalConfig, true, acpFSEnv(t, srv)...)
	c.prompt("run it")
	if len(c.terminal.creates) != 1 {
		t.Fatalf("terminal/create calls = %d, want 1", len(c.terminal.creates))
	}
	if !strings.Contains(string(srv.Requests()[1]), "client-terminal-ok") {
		t.Fatalf("client output missing from the turn")
	}
	if got := terminalEvents(t, e, c.p.sid); got != 0 {
		t.Fatalf("fallback events = %d, want 0", got)
	}
}

func TestACPClientTerminalFallback(t *testing.T) {
	e := newEnv(t)
	srv := providertest.New(t, nil,
		ToolCallReply("call-1", "shell", `{"command":"printf 'local-terminal-ok\\n'","timeout_s":10}`),
		providertest.Text("done"),
	)
	c := startClientTerm(t, e, terminalConfig, false, acpFSEnv(t, srv)...)
	c.prompt("run it")
	if len(c.terminal.creates) != 0 {
		t.Fatalf("terminal/create called without capability")
	}
	if !strings.Contains(string(srv.Requests()[1]), "local-terminal-ok") {
		t.Fatalf("local shell output missing from the turn")
	}
	if got := terminalEvents(t, e, c.p.sid); got != 1 {
		t.Fatalf("fallback events = %d, want 1", got)
	}
}

func TestACPClientTerminalKillOnTimeout(t *testing.T) {
	e := newEnv(t)
	srv := providertest.New(t, nil,
		ToolCallReply("call-1", "shell", `{"command":"sleep 5","timeout_s":1}`),
		providertest.Text("done"),
	)
	c := startClientTerm(t, e, terminalConfig, true, acpFSEnv(t, srv)...)
	c.prompt("run it")
	if c.terminal.kills != 1 {
		t.Fatalf("terminal/kill calls = %d, want 1", c.terminal.kills)
	}
	body := string(srv.Requests()[1])
	if !strings.Contains(body, "status timeout") || !strings.Contains(body, "signal SIGKILL") || !strings.Contains(body, "acp-client-terminal") {
		t.Fatalf("timeout result missing status/signal")
	}
}

func TestACPClientTerminalOutputCap(t *testing.T) {
	e := newEnv(t)
	srv := providertest.New(t, nil,
		ToolCallReply("call-1", "shell", `{"command":"yes a | head -c 1200000","timeout_s":10}`),
		providertest.Text("done"),
	)
	c := startClientTerm(t, e, terminalConfig, true, acpFSEnv(t, srv)...)
	c.prompt("run it")
	body := string(srv.Requests()[1])
	if !strings.Contains(body, "[truncated: client terminal output exceeded 1 MiB]") {
		t.Fatalf("cap marker missing from the turn")
	}
	if len(c.terminal.creates) != 1 {
		t.Fatalf("terminal/create calls = %d", len(c.terminal.creates))
	}
}

func TestACPClientTerminalNoSecret(t *testing.T) {
	e := newEnv(t)
	secret := "sk-abcdefghijklmnopqrstuvwxyz012345"
	cfg := terminalConfig + `
[exec]
env_allow = ["MY_SECRET"]
`
	srv := providertest.New(t, nil,
		ToolCallReply("call-1", "shell", `{"command":"printf '`+secret+`\\n'","timeout_s":10}`),
		providertest.Text("done"),
	)
	c := startClientTerm(t, e, cfg, true, append(acpFSEnv(t, srv), "MY_SECRET="+secret)...)
	c.prompt("run it")
	if len(c.terminal.creates) != 1 {
		t.Fatalf("terminal/create calls = %d", len(c.terminal.creates))
	}
	if env, ok := c.terminal.creates[0]["env"].([]any); ok {
		for _, e := range env {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			if m["name"] == "MY_SECRET" || strings.Contains(fmt.Sprint(m["value"]), secret) {
				t.Fatalf("secret env sent to the client: %+v", m)
			}
		}
	}
	for i, req := range srv.Requests() {
		if strings.Contains(string(req), secret) {
			t.Fatalf("secret in provider request %d", i)
		}
	}
	if !strings.Contains(string(srv.Requests()[1]), "[REDACTED") {
		t.Fatalf("secret output was not redacted")
	}
}

// terminalEvents counts acp_terminal_fallback notices for one session.
func terminalEvents(t *testing.T, e *Env, sid string) int {
	t.Helper()
	path := filepath.Join(fsDataDir(e), "damnbot.db")
	if _, err := os.Stat(path); err != nil {
		return 0
	}
	st, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: fsDataDir(e), Kind: "cli", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := st.Close(); cerr != nil {
			t.Errorf("close store: %v", cerr)
		}
	}()
	events, err := st.History(t.Context(), sid, 0, ctxdb.WithHistoryLimit(1000))
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for i := range events {
		if events[i].Kind == "system_notice" && strings.Contains(events[i].Payload, "acp_terminal_fallback") {
			n++
		}
	}
	return n
}

// TestACPClientTerminalDisabled proves use_client_terminal=false stays local
// and records no fallback event even when the client advertises terminal/*.
func TestACPClientTerminalDisabled(t *testing.T) {
	e := newEnv(t)
	cfg := acpFSConfig + `
[acp]
use_client_terminal = false
`
	srv := providertest.New(t, nil,
		ToolCallReply("call-1", "shell", `{"command":"printf 'local-disabled-ok\\n'","timeout_s":10}`),
		providertest.Text("done"),
	)
	c := startClientTerm(t, e, cfg, true, acpFSEnv(t, srv)...)
	c.prompt("run it")
	if len(c.terminal.creates) != 0 {
		t.Fatalf("terminal/create called while disabled")
	}
	if !strings.Contains(string(srv.Requests()[1]), "local-disabled-ok") {
		t.Fatalf("local shell output missing")
	}
	if got := terminalEvents(t, e, c.p.sid); got != 0 {
		t.Fatalf("fallback events = %d, want 0 when disabled", got)
	}
}
