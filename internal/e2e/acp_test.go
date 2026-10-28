package e2e

import (
	"encoding/json"
	"strings"
	"testing"
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
