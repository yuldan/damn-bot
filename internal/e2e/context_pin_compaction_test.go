package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

const (
	fact = "PINNED-FACT-4Q7"
	// pinSummaryMarker must appear in the compacted view of both sessions: it is
	// the proof that compaction actually rewrote the context.
	pinSummaryMarker = "the earlier conversation was compacted"
	// pinSummaryJSON is the strict five-key shape compaction.validate accepts.
	pinSummaryJSON = `{"goal":"continue after compaction","decisions":[],"files":[],"open_items":[],"facts":["` + pinSummaryMarker + `"]}`
	// fillers must push the fact beyond compaction's kept tail (keepTail = 8
	// newest model-visible messages), so the unpinned twin really is summarised
	// away while the pinned one stays verbatim.
	fillers = 6
)

// TestContextPinSurvivesCompaction (MEM-025) drives the real binary: a user
// fact is pinned with `sessions context-pin`, the transcript is then grown past
// the kept tail, the next turn is rejected by the provider as too long
// (context_length_exceeded) which forces a compaction and a retry, and the
// retried request must still carry the pinned fact.
//
// The control session repeats the same script without the pin: the fact was in
// its context before compaction (turn 1 request) and must be gone afterwards
// (only the summary survives), so the survival above can only come from the
// pin - that is what makes this test load-bearing. Both sessions must show the
// summary, otherwise the absence in the control would prove nothing.
func TestContextPinSurvivesCompaction(t *testing.T) {
	t.Parallel()
	e := New(t)

	// Session A: fact -> pin -> fillers -> forced compaction -> retry.
	e.Server.Push(providertest.Text("ack-1"))
	sid := startFactSession(t, e, fact)
	firstA := len(e.Requests()) - 1
	if !e.Contains(t, firstA, fact) {
		t.Fatalf("turn 1 request lacks the fact (harness/base assumption):\n%s", tail(e.RequestText(t, firstA)))
	}
	if pin := e.Exec("sessions", "context-pin", sid, "--at", "1", "--data-dir", e.DataDir); pin.Code != 0 || !strings.Contains(pin.Stdout, "pinned 1") {
		t.Fatalf("context-pin = %d %q (%s)", pin.Code, pin.Stdout, pin.Stderr)
	}
	if list := e.Exec("sessions", "context-pin", sid, "--data-dir", e.DataDir); list.Code != 0 || !strings.Contains(list.Stdout, "pin 1") {
		t.Fatalf("context-pin list = %d %q (%s)", list.Code, list.Stdout, list.Stderr)
	}
	fillerTurns(t, e, sid)
	compactTurn(t, e, sid)
	lastA := len(e.Requests()) - 1
	if !e.Contains(t, lastA, fact) {
		t.Errorf("pinned fact did not survive compaction (MEM-025):\n%s", tail(e.RequestText(t, lastA)))
	}
	if !e.Contains(t, lastA, pinSummaryMarker) {
		t.Errorf("pinned session shows no compaction summary, so survival proves nothing here:\n%s", tail(e.RequestText(t, lastA)))
	}

	// Session B (negative control): the same script without a pin.
	e.Server.Push(providertest.Text("ack-1"))
	control := startFactSession(t, e, fact)
	firstB := len(e.Requests()) - 1
	if !e.Contains(t, firstB, fact) {
		t.Fatalf("control turn 1 request lacks the fact:\n%s", tail(e.RequestText(t, firstB)))
	}
	fillerTurns(t, e, control)
	compactTurn(t, e, control)
	lastB := len(e.Requests()) - 1
	if e.Contains(t, lastB, fact) {
		t.Errorf("unpinned fact survived compaction, so the pin is not load-bearing:\n%s", tail(e.RequestText(t, lastB)))
	}
	if !e.Contains(t, lastB, pinSummaryMarker) {
		t.Errorf("control session shows no compaction summary, so the absence above proves nothing:\n%s", tail(e.RequestText(t, lastB)))
	}
	if lastB <= lastA {
		t.Fatalf("control session produced no requests of its own (lastA=%d lastB=%d)", lastA, lastB)
	}
}

// startFactSession runs one turn whose prompt carries fact and returns the
// session id the binary reported.
func startFactSession(t *testing.T, e *Env, fact string) string {
	t.Helper()
	var out struct {
		SessionID string `json:"session_id"`
	}
	res := e.Run("remember this durable fact: " + fact)
	res.RequireOK(t)
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil || out.SessionID == "" {
		t.Fatalf("turn 1 result: %v\nstdout:\n%s\nstderr:\n%s", err, res.Stdout, res.Stderr)
	}
	return out.SessionID
}

// fillerTurns appends pairs of visible messages until the fact sits before the
// tail compaction never summarises (keepTail = 8).
func fillerTurns(t *testing.T, e *Env, sid string) {
	t.Helper()
	for i := range fillers {
		e.Server.Push(providertest.Text("ack-filler"))
		res := e.Run("filler message "+string(rune('A'+i)), "--session", sid)
		res.RequireOK(t)
	}
}

// compactTurn makes the provider reject the next turn as too long, which forces
// stage-2 compaction and one retry: the summary reply deliberately does not
// contain the fact.
func compactTurn(t *testing.T, e *Env, sid string) {
	t.Helper()
	e.Server.Push(
		providertest.Status(400, "context_length_exceeded", ""),
		providertest.Text(pinSummaryJSON),
		providertest.Text("ack-2"),
	)
	n0 := len(e.Requests())
	res := e.Run("continue after the fact", "--session", sid)
	if res.Code != 0 {
		for i := n0; i < len(e.Requests()); i++ {
			t.Logf("turn-2 request %d: fact=%v summary=%v", i, e.Contains(t, i, fact), e.Contains(t, i, pinSummaryMarker))
		}
	}
	res.RequireOK(t)
	if !e.Contains(t, len(e.Requests())-1, pinSummaryMarker) {
		t.Fatalf("the retried turn was not assembled from the compacted view:\n%s", tail(e.RequestText(t, len(e.Requests())-1)))
	}
}

// tail returns the last dumpLimit bytes of s: a request body is dominated by the
// system prompt and the tool schemas, so failure dumps show the message tail
// where the assertions live.
func tail(s string) string {
	const dumpLimit = 1200
	if len(s) <= dumpLimit {
		return s
	}
	return "…" + s[len(s)-dumpLimit:]
}
