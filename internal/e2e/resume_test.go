package e2e

import (
	"encoding/json"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestRunResumeExcludesFailedTurn (acceptance 3): a failed turn that already
// executed a side effect is excluded from the next run on the same session,
// while the side effect itself remains observable.
func TestRunResumeExcludesFailedTurn(t *testing.T) {
	t.Parallel()
	e := New(t,
		ToolCallReply("c1", "write_file", `{"path":"side.txt","content":"effect"}`),
		providertest.Status(400, "invalid_request_error", ""),
		providertest.Text("resumed"),
	)
	first := e.Run("FAILED-TURN-MARKER write the file", "--approval", "auto-edit")
	if first.Code != 2 {
		t.Fatalf("first run exit = %d, want 2\nstdout:\n%s\nstderr:\n%s", first.Code, first.Stdout, first.Stderr)
	}
	if got := e.ReadFile("side.txt"); got != "effect" {
		t.Fatalf("side effect missing: %q", got)
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(first.Stdout), &out); err != nil || out.SessionID == "" {
		t.Fatalf("first run result: %v\n%s", err, first.Stdout)
	}
	second := e.Run("continue after failure", "--session", out.SessionID)
	second.RequireOK(t)
	reqs := e.Requests()
	if len(reqs) != 3 {
		t.Fatalf("provider calls = %d, want 2 (failed turn) + 1 (resume)", len(reqs))
	}
	body := e.RequestText(t, 2)
	for _, leaked := range []string{"FAILED-TURN-MARKER", "side.txt"} {
		if e.Contains(t, 2, leaked) {
			t.Errorf("resumed request leaked %q:\n%s", leaked, body)
		}
	}
	if !e.Contains(t, 2, "continue after failure") {
		t.Errorf("resumed request lacks the new prompt:\n%s", body)
	}
}

// TestRunContinueNewestSession (resume parity): -c/--continue picks the
// newest session in the current workspace and reuses its id.
func TestRunContinueNewestSession(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("first"), providertest.Text("second"))
	one := e.Run("first prompt")
	one.RequireOK(t)
	var first struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(one.Stdout), &first); err != nil || first.SessionID == "" {
		t.Fatalf("first result: %v\n%s", err, one.Stdout)
	}
	two := e.Run("second prompt", "--continue")
	two.RequireOK(t)
	var second struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(two.Stdout), &second); err != nil {
		t.Fatalf("second result: %v\n%s", err, two.Stdout)
	}
	if second.SessionID != first.SessionID {
		t.Fatalf("--continue opened %s, want %s", second.SessionID, first.SessionID)
	}
	if e.Contains(t, 1, "first prompt") != true || !e.Contains(t, 1, "second prompt") {
		t.Errorf("continued request lacks history/prompt: %s", e.RequestText(t, 1))
	}
}
