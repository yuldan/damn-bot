package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// sessionID decodes the run result's session id.
func sessionID(t *testing.T, stdout string) string {
	t.Helper()
	var out struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil || out.SessionID == "" {
		t.Fatalf("run result session id: %v\n%s", err, stdout)
	}
	return out.SessionID
}

// TestRunRecapOnResume (MEM-013): resuming prints a recap to stderr with the
// session, its event count and the last user message; stdout stays JSON.
func TestRunRecapOnResume(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("first"), providertest.Text("second"))
	first := e.Run("remember ALPHA")
	first.RequireOK(t)
	id := sessionID(t, first.Stdout)
	second := e.Run("and now BETA", "--continue")
	second.RequireOK(t)
	for _, want := range []string{"resumed " + id, "last user: remember ALPHA"} {
		if !strings.Contains(second.Stderr, want) {
			t.Errorf("recap lacks %q:\n%s", want, second.Stderr)
		}
	}
	if got := sessionID(t, second.Stdout); got != id {
		t.Errorf("resumed session = %s, want %s", got, id)
	}
	if !e.Contains(t, 1, "and now BETA") {
		t.Error("continued request lacks the new prompt")
	}
}

// TestRunRetryLastMessage (MEM-030): --retry resends the previous user
// message instead of requiring a prompt.
func TestRunRetryLastMessage(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("first"), providertest.Text("second"))
	first := e.Run("PING-RETRY")
	first.RequireOK(t)
	id := sessionID(t, first.Stdout)
	second := e.Run("", "--continue", "--retry")
	second.RequireOK(t)
	if got := sessionID(t, second.Stdout); got != id {
		t.Errorf("retry session = %s, want %s", got, id)
	}
	if !e.Contains(t, 1, "PING-RETRY") {
		t.Errorf("retry request does not resend the last message:\n%s", e.RequestText(t, 1))
	}
}

// TestRunRetrySurfacesSideEffects (#211): retrying a failed, excluded turn
// that already executed a mutating tool names the executed side effects on
// stderr before resending the message.
func TestRunRetrySurfacesSideEffects(t *testing.T) {
	t.Parallel()
	e := New(t,
		ToolCallReply("c1", "write_file", `{"path":"retry.txt","content":"first"}`),
		providertest.Status(400, "invalid_request_error", ""),
		ToolCallReply("c2", "write_file", `{"path":"retry.txt","content":"second"}`),
		providertest.Text("done"),
	)
	first := e.Run("create the side effect", "--approval", "auto-edit")
	if first.Code != 2 {
		t.Fatalf("first run exit = %d, want 2\nstderr:\n%s", first.Code, first.Stderr)
	}
	second := e.Run("", "--retry", "--approval", "auto-edit")
	second.RequireOK(t)
	if !strings.Contains(second.Stderr, "side effects already executed") || !strings.Contains(second.Stderr, "write_file(c1)") {
		t.Errorf("retry did not surface the executed side effects:\n%s", second.Stderr)
	}
	if !e.Contains(t, 2, "create the side effect") {
		t.Errorf("retry request does not resend the failed message:\n%s", e.RequestText(t, 2))
	}
}

// TestRunRetryRejectsPrompt: an explicit prompt with --retry is a usage error.
func TestRunRetryRejectsPrompt(t *testing.T) {
	t.Parallel()
	e := New(t)
	res := e.Run("please", "--retry")
	if res.Code != 2 || !strings.Contains(res.Stderr, "--retry") {
		t.Fatalf("exit = %d stderr = %q, want 2 with --retry usage", res.Code, res.Stderr)
	}
}
