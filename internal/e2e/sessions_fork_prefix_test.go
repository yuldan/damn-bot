package e2e

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// transcriptRow is one `sessions export --format jsonl` line (the export wire
// shape of cmd/damnbot). session is the session that owns the event, so the
// rows inherited from a parent keep the parent's id.
type transcriptRow struct {
	Session string `json:"session"`
	Seq     int64  `json:"seq"`
	Kind    string `json:"kind"`
	Role    string `json:"role"`
	Content string `json:"content"`
	Payload string `json:"payload"`
}

// seedTurns seeds parent "P" with two finished turns, in the event shape the
// loop writes (turn_start/user/assistant/turn_end), then closes the store so
// the child binary opens a committed database.
func seedTurns(t *testing.T, e *Env) {
	t.Helper()
	ctx := t.Context()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: e.DataDir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: "P", Origin: "headless", Cwd: "/w", Model: modelRef}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Append(ctx, "P", nil,
		ctxdb.Event{Kind: "turn_start", TurnID: "T1"},
		ctxdb.Event{Kind: "user_message", TurnID: "T1", Role: "user", Content: "ALPHA-MARKER-ONE"},
		ctxdb.Event{Kind: "assistant_message", TurnID: "T1", Role: "assistant", Content: "ACK-ONE"},
		ctxdb.Event{Kind: "turn_end", TurnID: "T1"},
		ctxdb.Event{Kind: "turn_start", TurnID: "T2"},
		ctxdb.Event{Kind: "user_message", TurnID: "T2", Role: "user", Content: "BETA-MARKER-TWO"},
		ctxdb.Event{Kind: "assistant_message", TurnID: "T2", Role: "assistant", Content: "ACK-TWO"},
		ctxdb.Event{Kind: "turn_end", TurnID: "T2"},
	); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

// sessionsCmd runs one `sessions` subcommand against the e2e data dir.
func sessionsCmd(t *testing.T, e *Env, args ...string) Result {
	t.Helper()
	full := append(append([]string{"sessions"}, args...), "--data-dir", e.DataDir)
	t.Logf("exec: %s", strings.Join(full, " "))
	return e.Exec(full...)
}

// exportJSONL runs the real binary's export and returns the raw stdout plus
// its decoded rows, so a failing assertion shows the bytes that were judged.
func exportJSONL(t *testing.T, e *Env, id string) (string, []transcriptRow) {
	t.Helper()
	res := sessionsCmd(t, e, "export", id, "--format", "jsonl")
	res.RequireOK(t)
	t.Logf("sessions export %s --format jsonl -> exit=%d\n%s", id, res.Code, res.Stdout)
	var rows []transcriptRow
	for i, line := range strings.Split(strings.TrimSuffix(res.Stdout, "\n"), "\n") {
		if line == "" {
			continue
		}
		var row transcriptRow
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("export %s row %d is not json: %v (%q)", id, i+1, err, line)
		}
		rows = append(rows, row)
	}
	return res.Stdout, rows
}

// forkChild runs `sessions fork`/`rewind` and returns the child id the CLI
// prints, with the raw line logged.
func forkChild(t *testing.T, e *Env, args ...string) string {
	t.Helper()
	res := sessionsCmd(t, e, args...)
	res.RequireOK(t)
	line := strings.TrimSpace(res.Stdout)
	i := strings.LastIndex(line, ": ")
	if i < 0 {
		t.Fatalf("sessions %s: no child id in %q", strings.Join(args, " "), line)
	}
	return line[i+2:]
}

// rowsUpTo is the sub-transcript a cut at maxSeq must expose: exactly the
// parent's own rows up to that seq.
func rowsUpTo(rows []transcriptRow, maxSeq int64) []transcriptRow {
	out := make([]transcriptRow, 0, len(rows))
	for _, row := range rows {
		if row.Seq <= maxSeq {
			out = append(out, row)
		}
	}
	return out
}

// requirePrefix asserts that got is exactly the parent's transcript up to the
// cut, and that the cut really dropped rows (a child that ignores the cut and
// copies everything must fail here).
func requirePrefix(t *testing.T, label string, got, parent []transcriptRow, maxSeq int64) {
	t.Helper()
	if want := rowsUpTo(parent, maxSeq); !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: child rows = %+v, want the parent's rows up to seq %d = %+v", label, got, maxSeq, want)
	}
	if len(got) == 0 || len(got) >= len(parent) {
		t.Fatalf("%s: child rows = %d of the parent's %d, want a strict non-empty prefix", label, len(got), len(parent))
	}
}

// TestE2EForkPrefixIsPrefixOfTheParent (scenario 7 of PR #239): the child of
// `sessions fork` exposes only the events before the cut, while the parent and
// another session keep their whole transcript.
func TestE2EForkPrefixIsPrefixOfTheParent(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("GAMMA-REPLY"))
	seedTurns(t, e)
	other := decodeRun(t, "sibling run", e.Run("GAMMA-MARKER")).SessionID
	parentRaw, parentRows := exportJSONL(t, e, "P")
	otherRaw, _ := exportJSONL(t, e, other)

	child := forkChild(t, e, "fork", "P", "--at", "turn:T1")
	childRaw, childRows := exportJSONL(t, e, child)
	requirePrefix(t, "fork at turn:T1", childRows, parentRows, 4)
	if !strings.Contains(childRaw, "ALPHA-MARKER-ONE") || strings.Contains(childRaw, "BETA-MARKER-TWO") {
		t.Errorf("child transcript is not the pre-fork prefix:\n%s", childRaw)
	}

	// Negative control 1: the parent keeps every row (append-only branch).
	afterRaw, afterRows := exportJSONL(t, e, "P")
	if afterRaw != parentRaw || !reflect.DeepEqual(afterRows, parentRows) {
		t.Errorf("parent export changed by the fork:\nbefore:\n%s\nafter:\n%s", parentRaw, afterRaw)
	}
	if !strings.Contains(afterRaw, "BETA-MARKER-TWO") {
		t.Errorf("parent lost its later turn:\n%s", afterRaw)
	}

	// Negative control 2: the session the real binary produced is untouched.
	otherAfter, _ := exportJSONL(t, e, other)
	if otherAfter != otherRaw || !strings.Contains(otherAfter, "GAMMA-MARKER") {
		t.Errorf("unrelated session changed by the fork:\nbefore:\n%s\nafter:\n%s", otherRaw, otherAfter)
	}

	// Load-bearing control: a fork at the head inherits the whole transcript,
	// so the rows missing above are missing because of the cut, not because a
	// child never inherits anything.
	head := forkChild(t, e, "fork", "P")
	headRaw, headRows := exportJSONL(t, e, head)
	if headRaw != parentRaw || !reflect.DeepEqual(headRows, parentRows) {
		t.Errorf("fork at head is not the whole transcript:\n%s", headRaw)
	}
}

// TestE2ERewindPrefixIsPrefixOfTheParent: rewind is a fork at an earlier point
// (SPEC §4.6): the child drops exactly the undone turn, and the parent keeps
// it.
func TestE2ERewindPrefixIsPrefixOfTheParent(t *testing.T) {
	t.Parallel()
	e := New(t)
	seedTurns(t, e)
	parentRaw, parentRows := exportJSONL(t, e, "P")

	child := forkChild(t, e, "rewind", "P", "--at", "turn:T2")
	childRaw, childRows := exportJSONL(t, e, child)
	requirePrefix(t, "rewind before turn:T2", childRows, parentRows, 4)
	if !strings.Contains(childRaw, "ALPHA-MARKER-ONE") || strings.Contains(childRaw, "BETA-MARKER-TWO") {
		t.Errorf("rewound child transcript keeps the undone turn:\n%s", childRaw)
	}
	afterRaw, afterRows := exportJSONL(t, e, "P")
	if afterRaw != parentRaw || !reflect.DeepEqual(afterRows, parentRows) {
		t.Errorf("rewind changed the parent:\nbefore:\n%s\nafter:\n%s", parentRaw, afterRaw)
	}
}

// markerSeq is the seq of the row carrying marker (the cut of the prefix a
// fork before that row must expose).
func markerSeq(t *testing.T, rows []transcriptRow, marker string) int64 {
	t.Helper()
	for _, row := range rows {
		if strings.Contains(row.Content, marker) {
			return row.Seq
		}
	}
	t.Fatalf("no row carries %q in %+v", marker, rows)
	return 0
}

// TestE2ERunSessionForkCutsAtTheAskedPoint drives the fork on a transcript the
// real binary produced: the child of a cut at the prompt row exposes the
// prompt and not the answer, cut 0 exposes nothing, and the default cut (head)
// exposes everything byte-identically. A single headless session holds one
// turn, so the middle cuts are covered by the seeded transcripts above.
func TestE2ERunSessionForkCutsAtTheAskedPoint(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("BETA-REPLY-MARKER"))
	sid := decodeRun(t, "run", e.Run("ALPHA-PROMPT-MARKER")).SessionID
	fullRaw, fullRows := exportJSONL(t, e, sid)
	if !strings.Contains(fullRaw, "ALPHA-PROMPT-MARKER") || !strings.Contains(fullRaw, "BETA-REPLY-MARKER") {
		t.Fatalf("run transcript lacks its own markers:\n%s", fullRaw)
	}

	cut := markerSeq(t, fullRows, "ALPHA-PROMPT-MARKER")
	child := forkChild(t, e, "fork", sid, "--at", strconv.FormatInt(cut, 10))
	childRaw, childRows := exportJSONL(t, e, child)
	requirePrefix(t, "fork at the prompt row", childRows, fullRows, cut)
	if !strings.Contains(childRaw, "ALPHA-PROMPT-MARKER") || strings.Contains(childRaw, "BETA-REPLY-MARKER") {
		t.Errorf("child transcript is not the prompt-only prefix:\n%s", childRaw)
	}

	empty := forkChild(t, e, "fork", sid, "--at", "0")
	emptyRaw, emptyRows := exportJSONL(t, e, empty)
	if len(emptyRows) != 0 || strings.TrimSpace(emptyRaw) != "" {
		t.Errorf("fork at seq 0 exposed rows:\n%s", emptyRaw)
	}

	head := forkChild(t, e, "fork", sid)
	headRaw, headRows := exportJSONL(t, e, head)
	if headRaw != fullRaw || !reflect.DeepEqual(headRows, fullRows) {
		t.Errorf("fork at head is not the whole transcript:\n%s", headRaw)
	}
	if afterRaw, _ := exportJSONL(t, e, sid); afterRaw != fullRaw {
		t.Errorf("parent export changed by the forks:\n%s", afterRaw)
	}
}
