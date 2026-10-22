package e2e

import (
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestCarrierReplayedNextIteration (acceptance 4a): an opaque reasoning
// carrier from iteration N is replayed byte-identically on N+1, on the wire,
// by the real binary.
func TestCarrierReplayedNextIteration(t *testing.T) {
	t.Parallel()
	const sig = "SIG-E2E-REPLAY"
	e := New(t,
		CarrierToolReply(sig, "c1", "todo", `{"items":[{"id":"1","text":"e2e","status":"pending"}]}`),
		providertest.Text("done"),
	)
	res := e.Run("exercise carrier replay")
	res.RequireOK(t)
	if got := len(e.Requests()); got != 2 {
		t.Fatalf("provider calls = %d, want 2", got)
	}
	if !e.Contains(t, 0, "exercise carrier replay") {
		t.Error("first request lacks the prompt")
	}
	if !e.Contains(t, 1, sig) {
		t.Errorf("second request did not replay the carrier %q:\n%s", sig, e.RequestText(t, 1))
	}
}

// TestCarrierRejectedNotReplayed (acceptance 4b): when replaying the carrier
// earns 400 invalid_signature, the retry request drops it, so the rejected
// carrier is not replayed again.
func TestCarrierRejectedNotReplayed(t *testing.T) {
	t.Parallel()
	const sig = "SIG-E2E-REJECT"
	e := New(t,
		CarrierToolReply(sig, "c1", "todo", `{"items":[{"id":"1","text":"e2e","status":"pending"}]}`),
		providertest.Status(400, "invalid_signature", ""),
		providertest.Text("done after rejection"),
	)
	res := e.Run("exercise carrier rejection")
	res.RequireOK(t)
	if got := len(e.Requests()); got != 3 {
		t.Fatalf("provider calls = %d, want 3 (carrier, rejected replay, retry)", got)
	}
	if !e.Contains(t, 1, sig) {
		t.Errorf("second request did not replay the carrier %q", sig)
	}
	if e.Contains(t, 2, sig) {
		t.Errorf("retry after invalid_signature replayed the rejected carrier:\n%s", e.RequestText(t, 2))
	}
}

// TestHarnessSmoke proves the shared harness surface end to end: a real tool
// call, request decoding and workspace read/write helpers.
func TestHarnessSmoke(t *testing.T) {
	t.Parallel()
	e := New(t,
		ToolCallReply("c1", "read_file", `{"path":"note.txt"}`),
		providertest.Text("read it"),
	)
	e.WriteFile("note.txt", "hello e2e")
	if got := e.ReadFile("note.txt"); got != "hello e2e" {
		t.Fatalf("fixture file = %q", got)
	}
	res := e.Run("read the note")
	res.RequireOK(t)
	if got := len(e.Requests()); got != 2 {
		t.Fatalf("provider calls = %d, want 2", got)
	}
	body := e.RequestJSON(t, 0)
	if body["model"] != "gpt-fake" {
		t.Fatalf("request model = %v", body["model"])
	}
	if got := e.ReadFile("note.txt"); got != "hello e2e" {
		t.Fatalf("read_file changed the workspace file: %q", got)
	}
	if e.Path("note.txt") == "" {
		t.Fatal("empty workspace path")
	}
}
