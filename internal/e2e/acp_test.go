package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestACPStdio starts the real binary as an ACP stdio server and checks the
// initialize/session-new/unknown-method round trip (SPEC §13.3).
func TestACPStdio(t *testing.T) {
	t.Parallel()
	e := New(t)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"session/new","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"nope","params":{}}`,
	}, "\n") + "\n"
	res := e.ExecStdin(input, "acp")
	if res.Code != 0 {
		t.Fatalf("acp exit = %d\nstdout:\n%s\nstderr:\n%s", res.Code, res.Stdout, res.Stderr)
	}
	lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
	if len(lines) != 3 {
		t.Fatalf("frames = %d, want 3\n%s", len(lines), res.Stdout)
	}
	var initResp struct {
		ID     int `json:"id"`
		Result struct {
			ProtocolVersion int `json:"protocolVersion"`
			AgentInfo       struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"agentInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[0]), &initResp); err != nil {
		t.Fatalf("initialize response = %s", lines[0])
	}
	if initResp.ID != 1 || initResp.Result.ProtocolVersion != 1 || initResp.Result.AgentInfo.Name != "damnbot" || initResp.Result.AgentInfo.Version == "" {
		t.Fatalf("initialize response = %s", lines[0])
	}
	var newResp struct {
		Result struct {
			SessionID string `json:"sessionId"`
			Modes     struct {
				CurrentModeID  string `json:"currentModeId"`
				AvailableModes []struct {
					ID string `json:"id"`
				} `json:"availableModes"`
			} `json:"modes"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(lines[1]), &newResp); err != nil || newResp.Result.SessionID == "" {
		t.Fatalf("session/new response = %s", lines[1])
	}
	if newResp.Result.Modes.CurrentModeID != "ask" || len(newResp.Result.Modes.AvailableModes) != 4 {
		t.Fatalf("session/new modes = %s", lines[1])
	}
	var errResp struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(lines[2]), &errResp); err != nil || errResp.Error.Code != -32601 {
		t.Fatalf("unknown-method response = %s", lines[2])
	}
}

// acpProc is the real binary running `acp` with pipes, so a scenario can
// answer session/request_permission while the server waits.
type acpProc struct {
	t    *testing.T
	cmd  *exec.Cmd
	in   io.WriteCloser
	out  *bufio.Reader
	errb *bytes.Buffer
	sid  string
}

// appConfigDir mirrors internal/config baseDirs for the harness HOME.
func appConfigDir(home string) string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(home, "AppData", "Roaming", "damnbot")
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "damnbot")
	}
	return filepath.Join(home, "damnbot")
}

// startACP writes the isolated config and starts `damnbot acp` in Workdir.
func startACP(t *testing.T, e *Env, extraConfig string) *acpProc {
	t.Helper()
	dir := appConfigDir(e.home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(extraConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	cmd := exec.CommandContext(ctx, binPaths.main, "acp") // #nosec G204 -- test-built binary
	cmd.Dir = e.Workdir
	cmd.Env = append(os.Environ(),
		"HOME="+e.home,
		"XDG_CONFIG_HOME="+e.home,
		"USERPROFILE="+e.home,
		"APPDATA="+filepath.Join(e.home, "AppData", "Roaming"),
		"LOCALAPPDATA="+filepath.Join(e.home, "AppData", "Local"),
	)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	errb := &bytes.Buffer{}
	cmd.Stderr = errb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &acpProc{t: t, cmd: cmd, in: in, out: bufio.NewReader(out), errb: errb}
	t.Cleanup(func() {
		cancel()
		if err := in.Close(); err != nil {
			t.Errorf("close stdin: %v", err)
		}
		if err := cmd.Wait(); err != nil {
			if _, exited := errors.AsType[*exec.ExitError](err); !exited && !errors.Is(err, context.Canceled) {
				t.Errorf("wait: %v", err)
			}
		}
	})
	return p
}

func (p *acpProc) send(v any) {
	p.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		p.t.Fatal(err)
	}
	if _, err := p.in.Write(append(b, '\n')); err != nil {
		p.t.Fatalf("write frame: %v (stderr %s)", err, p.errb.String())
	}
}

func (p *acpProc) recv() map[string]any {
	p.t.Helper()
	line, err := p.out.ReadString('\n')
	if err != nil {
		p.t.Fatalf("read frame: %v (stderr %s)", err, p.errb.String())
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(line), &v); err != nil {
		p.t.Fatalf("frame %q: %v", line, err)
	}
	return v
}

// initSession performs initialize and session/new at cwd.
func (p *acpProc) initSession(cwd string) {
	p.t.Helper()
	p.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": 1}})
	f := p.recv()
	if f["id"] != float64(1) || f["result"] == nil {
		p.t.Fatalf("initialize = %v", f)
	}
	p.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": map[string]any{"cwd": cwd}})
	f = p.recv()
	res, ok := f["result"].(map[string]any)
	if !ok {
		p.t.Fatalf("session/new = %v", f)
	}
	sid, ok := res["sessionId"].(string)
	if f["id"] != float64(2) || !ok || sid == "" {
		p.t.Fatalf("session/new = %v", f)
	}
	p.sid = sid
}

func acpError(t *testing.T, f map[string]any) (int, string) {
	t.Helper()
	e, ok := f["error"].(map[string]any)
	if !ok {
		t.Fatalf("frame is not an error: %v", f)
	}
	c, ok := e["code"].(float64)
	if !ok {
		t.Fatalf("error code is not a number: %v", e)
	}
	code := int(c)
	errorCode := ""
	if data, ok := e["data"].(map[string]any); ok {
		if v, ok := data["error_code"].(string); ok {
			errorCode = v
		}
	}
	return code, errorCode
}

// TestACPResourcePrompt checks the inbound resource parser on the real
// binary: a valid resource reaches the turn (which then fails for lack of a
// configured provider, not for params); broken resources are -32602.
func TestACPResourcePrompt(t *testing.T) {
	e := newEnv(t)
	p := startACP(t, e, "[approval]\npreview_timeout_s = 1\n")
	p.initSession(e.Workdir)

	p.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": map[string]any{
		"sessionId": p.sid,
		"prompt":    []any{map[string]any{"type": "resource", "resource": map[string]any{"uri": "file:///note.txt", "text": "body"}}},
	}})
	f := p.recv()
	// The turn may stream updates first; find its response.
	for f["id"] != float64(3) {
		f = p.recv()
	}
	if f["error"] == nil {
		t.Fatalf("turn without a provider unexpectedly succeeded: %v", f)
	}
	if code, _ := acpError(t, f); code == -32602 {
		t.Fatalf("valid resource rejected as params: %v", f)
	}

	broken := []struct {
		id    int
		block map[string]any
		code  string
	}{
		{4, map[string]any{"type": "resource", "resource": map[string]any{"text": "no uri"}}, "invalid_resource"},
		{5, map[string]any{"type": "resource", "resource": map[string]any{"uri": "file:///x", "text": strings.Repeat("a", 4001)}}, "too_large"},
	}
	for _, tc := range broken {
		p.send(map[string]any{"jsonrpc": "2.0", "id": tc.id, "method": "session/prompt", "params": map[string]any{"sessionId": p.sid, "prompt": []any{tc.block}}})
		code, errorCode := acpError(t, p.recv())
		if code != -32602 || errorCode != tc.code {
			t.Fatalf("resource %d = %d/%q, want -32602/%q", tc.id, code, errorCode, tc.code)
		}
	}
}
