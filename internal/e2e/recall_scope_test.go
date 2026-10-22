package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// Skenario 2 (body PR #239): the per-turn recall tier (SPEC §4.5 row 6) is
// scoped to the running session's lineage. A transcript of another project or
// of another gateway principal sits in the same FTS5 index the tier queries and
// matches the tier's own query text, yet never reaches a request (ADR-134,
// SEC12). Both tests drive the real binary through the forced-compaction
// scenario, so the tier's row-6 gate is open on the retried request.
//
// The positive half of the scenario — a snippet recalled from an *earlier turn*
// of the running session and rendered inside `<untrusted source="recall:...">`
// — is not reachable through `run` on this base: run always mints a root
// session (newRunSession, no parent), and the loop mints one turn id per turn
// (loop.Prompt), so every event the run writes carries the running turn's id
// and hitVisible drops all of them. The same fixture becomes the positive once
// the resume CLI (run --session/--continue) or sessions fork lands.
const (
	// recallQuery is the running turn. It is also the tier's FTS5 query, whose
	// terms are AND-ed, so a matching transcript must carry every word.
	recallQuery = "find the alpha note"
	// recallMarkerOwn sits in the run's own earlier turn: the transcript the
	// compaction summarises away.
	recallMarkerOwn = "RECALLALPHA7"
	// recallNote is that earlier turn's text.
	recallNote = "the alpha note to find holds " + recallMarkerOwn + " and nothing else"
	// recallForeignNote carries the tier's own query terms, so an unfiltered
	// search of the same index really does match a seeded transcript.
	recallForeignNote = "the alpha note to find holds "
	// Markers of the seeded out-of-lineage transcripts: another project,
	// another gateway principal under the running cwd, and another session of
	// the running project under the local principal.
	recallMarkerProject   = "RECALLBRAVO8"
	recallMarkerPrincipal = "RECALLCHARLIE9"
	recallMarkerLocal     = "RECALLDELTA10"
	// recallRetryRequest is the request the loop assembles after the forced
	// stage-2 compaction: five tool rounds (0–4), the rejected request (5),
	// the summary call (6), then the retry (7).
	recallRetryRequest = 7
)

// recallScript scripts the forced compaction both tests share: five read_file
// rounds (the first result holds the marker), the provider rejecting the next
// request as too long, the stage-2 summary, and the retried answer.
func recallScript() []providertest.Reply {
	out := make([]providertest.Reply, 0, 8)
	for i := range 5 {
		out = append(out, ToolCallReply(fmt.Sprintf("c%d", i+1), "read_file", fmt.Sprintf(`{"path":"note%d.txt"}`, i+1)))
	}
	return append(out,
		providertest.Status(400, "context_length_exceeded", ""),
		providertest.Text(summaryJSON),
		providertest.Text("done"),
	)
}

// recallScenario starts the shared scenario: the scripted provider and the five
// notes the run reads. Only note1 carries the query terms and the marker; the
// compaction summarises that turn away, so the retry is the request where the
// recall tier is eligible (SPEC §4.5 row 6).
func recallScenario(t *testing.T) *Env {
	t.Helper()
	e := New(t, recallScript()...)
	e.WriteFile("note1.txt", recallNote)
	for i := 2; i <= 5; i++ {
		e.WriteFile(fmt.Sprintf("note%d.txt", i), fmt.Sprintf("filler note %d, no marker here", i))
	}
	return e
}

// runRecallScenario runs the child turn, asserts the scenario reached the
// retried request, and returns the running session's id (from --output json).
func runRecallScenario(t *testing.T, e *Env) string {
	t.Helper()
	res := e.Run(recallQuery, "--approval", "auto")
	res.RequireOK(t)
	if got := len(e.Requests()); got != 8 {
		t.Fatalf("provider calls = %d, want 8 (5 rounds, rejected, summary, retry)", got)
	}
	return runSessionID(t, res)
}

// runSessionID reads the session id the child reported.
func runSessionID(t *testing.T, res Result) string {
	t.Helper()
	for line := range strings.Lines(res.Stdout) {
		var out struct {
			SessionID string `json:"session_id"`
		}
		if json.Unmarshal([]byte(line), &out) == nil && out.SessionID != "" {
			return out.SessionID
		}
	}
	t.Fatalf("run output has no session_id:\n%s", res.Stdout)
	return ""
}

// requireRecallGateOpen fails unless the retried request carries the stage-2
// summary: with an active summary the row-6 trigger held at that request, so
// the tier really ran there (ADR-134).
func requireRecallGateOpen(t *testing.T, e *Env) {
	t.Helper()
	if !e.Contains(t, recallRetryRequest, "[summary of events") ||
		!e.Contains(t, recallRetryRequest, `<untrusted source=\"summary\">`) {
		t.Fatalf("the retry carries no active summary (row-6 trigger):\n%.900s",
			e.RequestText(t, recallRetryRequest))
	}
}

// recallQueryHits fails unless an unfiltered search with the tier's own query
// text finds every given session in the scenario's index: the fixtures are live
// in the very index the tier queries, so a transcript's absence from the
// requests is the tier's scope and not a missing match (SPEC §4.6).
func recallQueryHits(t *testing.T, e *Env, ids ...string) {
	t.Helper()
	found := make(map[string]bool, len(ids))
	err := recallStore(t, e, func(db *ctxdb.Store) error {
		hits, serr := db.Search(t.Context(), ctxdb.Query{Text: recallQuery})
		if serr != nil {
			return fmt.Errorf("search scenario index: %w", serr)
		}
		for i := range hits {
			found[hits[i].SessionID] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if !found[id] {
			t.Fatalf("fixture is not load-bearing: %q does not match %s in FTS", recallQuery, id)
		}
	}
}

// forbidRecall fails when any request of the scenario carries marker.
func forbidRecall(t *testing.T, e *Env, marker, why string) {
	t.Helper()
	for i := range e.Requests() {
		if e.Contains(t, i, marker) {
			t.Errorf("request %d carries %s (%s):\n%.600s", i, marker, why, e.RequestText(t, i))
		}
	}
}

// TestRecallIgnoresAnotherProject is scenario 2's negative control on the
// project axis: a transcript seeded in the same data dir under another cwd
// matches the tier's own query in the same index and is never shown.
func TestRecallIgnoresAnotherProject(t *testing.T) {
	t.Parallel()
	e := recallScenario(t)
	seedForeignRecall(t, e, "OTHERPROJECT", foreignRecall{
		origin: "headless", cwd: "/other-project", content: recallForeignNote + recallMarkerProject,
	})
	sid := runRecallScenario(t, e)
	requireRecallGateOpen(t, e)
	recallQueryHits(t, e, sid, "OTHERPROJECT")
	forbidRecall(t, e, recallMarkerProject, "another project's transcript")
}

// TestRecallIgnoresAnotherPrincipal is scenario 2's negative control on the
// principal axis: a transcript seeded under the running project's own cwd but
// another gateway principal — and one under the same cwd and local principal —
// both match the tier's query in the same index, and neither is ever shown. The
// same-cwd session under the local principal is the control for the project
// axis itself: the tier is scoped to the lineage, not to every session of the
// project.
func TestRecallIgnoresAnotherPrincipal(t *testing.T) {
	t.Parallel()
	e := recallScenario(t)
	seedForeignRecall(t, e, "OTHERGATEWAY", foreignRecall{
		origin: "headless", cwd: e.Workdir, identity: "tg:other-principal",
		content: recallForeignNote + recallMarkerPrincipal,
	})
	seedForeignRecall(t, e, "OTHERLOCAL", foreignRecall{
		origin: "headless", cwd: e.Workdir, content: recallForeignNote + recallMarkerLocal,
	})
	sid := runRecallScenario(t, e)
	requireRecallGateOpen(t, e)
	recallQueryHits(t, e, sid, "OTHERGATEWAY", "OTHERLOCAL")
	if got := foreignRecallIdentity(t, e, "OTHERGATEWAY"); got != "tg:other-principal" {
		t.Fatalf("fixture identity = %q, want tg:other-principal", got)
	}
	forbidRecall(t, e, recallMarkerPrincipal, "another gateway principal under the same cwd")
	forbidRecall(t, e, recallMarkerLocal, "another session of the same project")
}

// foreignRecall is one transcript seeded beside the session the child creates.
type foreignRecall struct {
	cwd, origin, identity, content string
}

// seedForeignRecall appends one transcript in another session of the scenario's
// data dir, in the same FTS index the recall tier queries. It runs before the
// child starts, so the transcript is there for every request of the turn.
func seedForeignRecall(t *testing.T, e *Env, id string, f foreignRecall) {
	t.Helper()
	err := recallStore(t, e, func(db *ctxdb.Store) error {
		return db.Write(t.Context(), func(tx *ctxdb.Tx) error {
			if _, cerr := tx.CreateSession(ctxdb.Session{ID: id, Origin: f.origin, Cwd: f.cwd}); cerr != nil {
				return fmt.Errorf("create session %s: %w", id, cerr)
			}
			if f.identity != "" {
				if _, xerr := tx.Exec(`INSERT INTO session_labels(session_id, key, value, created_at)
					VALUES (?1, 'gateway_chat', ?2, 0)`, id, f.identity); xerr != nil {
					return fmt.Errorf("label session %s: %w", id, xerr)
				}
			}
			if _, aerr := tx.AppendEvent(id, &ctxdb.Event{
				Kind: "user_message", Role: "user", TurnID: id + "-t1", Content: f.content,
			}); aerr != nil {
				return fmt.Errorf("append transcript %s: %w", id, aerr)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

// foreignRecallIdentity reads the gateway principal of a seeded session.
func foreignRecallIdentity(t *testing.T, e *Env, id string) string {
	t.Helper()
	var out string
	err := recallStore(t, e, func(db *ctxdb.Store) error {
		return db.Write(t.Context(), func(tx *ctxdb.Tx) error {
			return tx.Get(`SELECT value FROM session_labels WHERE session_id = ?1 AND key = 'gateway_chat'
				ORDER BY id DESC LIMIT 1`, []any{id}, &out)
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// recallStore runs fn against the scenario's data dir and closes the store
// before the child process starts.
func recallStore(t *testing.T, e *Env, fn func(db *ctxdb.Store) error) error {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: e.DataDir, Kind: "run"})
	if err != nil {
		t.Fatalf("open scenario store: %v", err)
	}
	derr := fn(db)
	if cerr := db.Close(); cerr != nil {
		t.Errorf("close scenario store: %v", cerr)
	}
	return derr
}
