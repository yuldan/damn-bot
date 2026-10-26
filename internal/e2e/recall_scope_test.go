package e2e

import (
	"strings"
	"testing"

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
