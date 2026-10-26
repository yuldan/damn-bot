package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestRecallStaysInLineage (W7 security boundary): the per-turn recall tier
// runs only after compaction (SPEC §4.5 row-6) and its scope is the session's
// own lineage — compaction_wire.go:170 takes db.Lineage, :184 lineageBounds
// keeps the target and same-origin ancestors, :221 searchLineage searches only
// the ids that map carries. A fact written by an earlier, unrelated session of
// the same project must never ride into the prompt through retrieval, even
// when the turn's own text matches that session's event verbatim.
//
// The scenario drives one turn into compaction mid-flight (five trusted reads,
// then the provider rejects the request as too long, so stage 2 summarises and
// the turn continues); the post-compaction request is the one the recall tier
// feeds. The turn prompt is kept to the two FTS terms of the foreign event
// ("codename zebra") so the query does match it: the only thing keeping it out
// of the prompt is the lineage scope. The negative control drops the per-session
// filter (SessionID: id -> all sessions) and the foreign fact must then appear,
// which is what pins the isolation to the scope filter.
func TestRecallStaysInLineage(t *testing.T) {
	t.Parallel()
	const foreign = "ZQXCROSSA1122" // session A: same project, another session
	script := append([]providertest.Reply{providertest.Text("noted")},
		append(readNotes(),
			providertest.Status(400, "context_length_exceeded", ""),
			providertest.Text(summaryJSON),
			providertest.Text("done"))...)

	env := New(t, script...)

	// Session A: an unrelated earlier session of the same project and cwd (run
	// starts a fresh session per invocation, run.go:421). Its event carries the
	// two FTS terms the recalling turn will query.
	if res := env.Run("the codename " + foreign + " zebra"); res.Code != 0 {
		t.Fatalf("session A run = %d\n%s%s", res.Code, res.Stdout, res.Stderr)
	}

	// Session B: the turn whose post-compaction request carries the recall tier.
	res := env.Run("codename zebra")
	res.RequireOK(t)

	reqs := env.Requests()
	if len(reqs) < 2 {
		t.Fatalf("provider calls = %d, want the session A turn plus the session B stages", len(reqs))
	}
	last := env.RequestText(t, len(reqs)-1)
	if strings.Contains(last, foreign) {
		t.Fatalf("recall leaked another session's fact into the prompt: %s", foreign)
	}
	// No in-scope hit can exist here, and that is by design: a fresh session's
	// only events belong to the running turn, which hitVisible drops (SEC12-2),
	// and run never adopts a forked session (parseRunFlags has no
	// --session/--fork/--continue; run.go creates the session). The foreign fact
	// is therefore checked against a query the index can answer: the
	// load-bearing control drops the per-session filter and the fact does appear,
	// which shows the query matched and the scope stopped it.
}

// --- Ported from #283 (bcf1484e) -------------------------------------------------
//
// The three isolation fixtures of #283 live on from `main`'s style: each asserts
// the *last* request of the session (the one the recall tier feeds) and carries a
// vacuity guard, so no fixed request index is baked in. `TestRecallStaysInLineage`
// above (the project/lineage half #220 landed) stays untouched.

const (
	// scopeQuery is the running turn. It is also the tier's FTS5 query, whose
	// terms are AND-ed, so a matching transcript must carry every word.
	scopeQuery = "find the alpha note"
	// scopeForeignText carries every query term: a seeded transcript really does
	// match an unfiltered search of the same index.
	scopeForeignText = "the alpha note to find holds "
	// Markers of the seeded out-of-lineage transcripts: another project, another
	// gateway principal under the running cwd, and another session of the
	// running project under the local principal.
	scopeMarkerProject   = "SCOPEBRAVO8"
	scopeMarkerPrincipal = "SCOPECHARLIE9"
	scopeMarkerLocal     = "SCOPEDELTA10"
)

// scopeScript scripts the forced compaction every fixture shares: the five note
// reads of the scenario, the provider rejecting the next request as too long,
// the stage-2 summary, and the retried answer.
func scopeScript() []providertest.Reply {
	return append(readNotes(),
		providertest.Status(400, "context_length_exceeded", ""),
		providertest.Text(summaryJSON),
		providertest.Text("done"),
	)
}

// scopeScenario starts the shared scenario: the scripted provider, the five
// notes (seedNotes from the taint scenario) and the running turn.
func scopeScenario(t *testing.T) *Env {
	t.Helper()
	e := New(t, scopeScript()...)
	seedNotes(e)
	return e
}

// runScopeTurn runs the child turn and returns its session id (from --output
// json) together with the index of the last provider request.
func runScopeTurn(t *testing.T, e *Env) (string, int) {
	t.Helper()
	res := e.Run(scopeQuery, "--approval", "auto")
	res.RequireOK(t)
	reqs := e.Requests()
	want := len(scopeScript())
	if len(reqs) != want {
		t.Fatalf("provider calls = %d, want %d (five rounds, rejected, summary, retry)", len(reqs), want)
	}
	return scopeSessionID(t, res), len(reqs) - 1
}

// scopeSessionID reads the session id the child reported.
func scopeSessionID(t *testing.T, res Result) string {
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

// requireScopeGateOpen fails unless the last request carries the stage-2
// summary: with an active summary the row-6 trigger held there, so the tier
// really ran at that request (ADR-134).
func requireScopeGateOpen(t *testing.T, e *Env, last int) {
	t.Helper()
	if !e.Contains(t, last, "[summary of events") || !e.Contains(t, last, `<untrusted source=\"summary\">`) {
		t.Fatalf("the last request carries no active summary (row-6 trigger):\n%.900s", e.RequestText(t, last))
	}
}

// scopeVacuity fails unless an unfiltered search with the tier's own query text
// finds every given session in the scenario's index: the fixtures are live in
// the very index the tier queries, so a transcript's absence from the request is
// the scope's doing, not a missing match (SPEC §4.6).
func scopeVacuity(t *testing.T, e *Env, ids ...string) {
	t.Helper()
	found := make(map[string]bool, len(ids))
	err := scopeStore(t, e, func(db *ctxdb.Store) error {
		hits, serr := db.Search(t.Context(), ctxdb.Query{Text: scopeQuery})
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
			t.Fatalf("fixture is not load-bearing: %q does not match %q in FTS", id, scopeQuery)
		}
	}
}

// forbidInLast fails when the last request carries any marker.
func forbidInLast(t *testing.T, e *Env, last int, why string, markers ...string) {
	t.Helper()
	body := e.RequestText(t, last)
	for _, m := range markers {
		if strings.Contains(body, m) {
			t.Errorf("the last request carries %s (%s):\n%.600s", m, why, body)
		}
	}
}

// foreignScope is one transcript seeded beside the session the child creates.
type foreignScope struct {
	id, cwd, identity, content string
}

// seedForeignScope appends one transcript in another session of the scenario's
// data dir, in the same FTS index the recall tier queries. It runs before the
// child starts, so the transcript is there for every request of the turn.
func seedForeignScope(t *testing.T, e *Env, f foreignScope) {
	t.Helper()
	err := scopeStore(t, e, func(db *ctxdb.Store) error {
		return db.Write(t.Context(), func(tx *ctxdb.Tx) error {
			if _, cerr := tx.CreateSession(ctxdb.Session{ID: f.id, Origin: "headless", Cwd: f.cwd}); cerr != nil {
				return fmt.Errorf("create session %s: %w", f.id, cerr)
			}
			if f.identity != "" {
				if _, xerr := tx.Exec(`INSERT INTO session_labels(session_id, key, value, created_at)
					VALUES (?1, 'gateway_chat', ?2, 0)`, f.id, f.identity); xerr != nil {
					return fmt.Errorf("label session %s: %w", f.id, xerr)
				}
			}
			if _, aerr := tx.AppendEvent(f.id, &ctxdb.Event{
				Kind: "user_message", Role: "user", TurnID: f.id + "-t1", Content: f.content,
			}); aerr != nil {
				return fmt.Errorf("append transcript %s: %w", f.id, aerr)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

// scopeIdentity reads the gateway principal of a seeded session.
func scopeIdentity(t *testing.T, e *Env, id string) string {
	t.Helper()
	var out string
	err := scopeStore(t, e, func(db *ctxdb.Store) error {
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

// scopeStore runs fn against the scenario's data dir and closes the store
// before the child process starts.
func scopeStore(t *testing.T, e *Env, fn func(db *ctxdb.Store) error) error {
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

// TestRecallIgnoresAnotherProject: a transcript seeded in the same data dir under
// another cwd matches the tier's own query in the same index and never reaches
// the last request.
func TestRecallIgnoresAnotherProject(t *testing.T) {
	t.Parallel()
	e := scopeScenario(t)
	seedForeignScope(t, e, foreignScope{
		id: "SCOPEOTHERPROJECT", cwd: "/other-project", content: scopeForeignText + scopeMarkerProject,
	})
	sid, last := runScopeTurn(t, e)
	requireScopeGateOpen(t, e, last)
	scopeVacuity(t, e, sid, "SCOPEOTHERPROJECT")
	forbidInLast(t, e, last, "another project's transcript", scopeMarkerProject)
}

// TestRecallIgnoresAnotherPrincipal: a transcript under the running project's own
// cwd but another gateway principal matches the tier's query in the same index
// and never reaches the last request.
func TestRecallIgnoresAnotherPrincipal(t *testing.T) {
	t.Parallel()
	e := scopeScenario(t)
	seedForeignScope(t, e, foreignScope{
		id: "SCOPEOTHERGATEWAY", cwd: e.Workdir, identity: "tg:other-principal",
		content: scopeForeignText + scopeMarkerPrincipal,
	})
	sid, last := runScopeTurn(t, e)
	requireScopeGateOpen(t, e, last)
	scopeVacuity(t, e, sid, "SCOPEOTHERGATEWAY")
	if got := scopeIdentity(t, e, "SCOPEOTHERGATEWAY"); got != "tg:other-principal" {
		t.Fatalf("fixture identity = %q, want tg:other-principal", got)
	}
	forbidInLast(t, e, last, "another gateway principal under the same cwd", scopeMarkerPrincipal)
}

// TestRecallIgnoresAnotherSessionOfProject: the tier is scoped to the lineage,
// not to every session of the project — a sibling session under the same cwd and
// the local principal is never shown either.
func TestRecallIgnoresAnotherSessionOfProject(t *testing.T) {
	t.Parallel()
	e := scopeScenario(t)
	seedForeignScope(t, e, foreignScope{
		id: "SCOPEOTHERLOCAL", cwd: e.Workdir, content: scopeForeignText + scopeMarkerLocal,
	})
	sid, last := runScopeTurn(t, e)
	requireScopeGateOpen(t, e, last)
	scopeVacuity(t, e, sid, "SCOPEOTHERLOCAL")
	forbidInLast(t, e, last, "another session of the same project", scopeMarkerLocal)
}
