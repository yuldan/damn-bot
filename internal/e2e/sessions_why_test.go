package e2e

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// turnID returns the turn id of the turn_start event of session sid.
func turnID(t *testing.T, dataDir, sid string) string {
	t.Helper()
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dataDir, Kind: "cli"})
	if err != nil {
		t.Fatalf("open ctxdb: %v", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	var id string
	err = db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT turn_id FROM events WHERE session_id = ? AND kind = 'turn_start' ORDER BY id LIMIT 1`, []any{sid}, &id)
	})
	if err != nil {
		t.Fatalf("read turn id: %v", err)
	}
	return id
}

// journalRows counts turn_journal rows (deleted when a turn ends).
func journalRows(t *testing.T, dataDir string) int64 {
	t.Helper()
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dataDir, Kind: "cli"})
	if err != nil {
		t.Fatalf("open ctxdb: %v", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	var n int64
	err = db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT count(*) FROM turn_journal`, nil, &n)
	})
	if err != nil {
		t.Fatalf("count turn_journal: %v", err)
	}
	return n
}

func fileHash(t *testing.T, p string) [sha256.Size]byte {
	t.Helper()
	b, err := os.ReadFile(p) //nolint:gosec // G304: harness temp path
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return sha256.Sum256(b)
}

// TestSessionsWhyChain: the decision chain of a finished turn is rendered from
// events alone; --session is an optional filter; unknown turns are exit 1.
func TestSessionsWhyChain(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("first reply"), providertest.Text("second reply"))
	e.Run("explain the chain").RequireOK(t)
	e.Run("other session").RequireOK(t)
	ids := e.sessionIDs()
	if len(ids) != 2 {
		t.Fatalf("sessions ls = %v, want 2 ids", ids)
	}
	sid := ids[0]
	turn := turnID(t, e.DataDir, sid)
	if turn == "" {
		t.Fatal("turn_start has no turn id")
	}
	if n := journalRows(t, e.DataDir); n != 0 {
		t.Fatalf("turn_journal still has %d rows; the chain must come from events", n)
	}
	res := e.Exec("sessions", "why", turn, "--data-dir", e.DataDir)
	res.RequireOK(t)
	for _, want := range []string{"session " + sid, "turn " + turn, "turn_start", "llm_call", "turn_end", "stop_reason="} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("why output lacks %q:\n%s", want, res.Stdout)
		}
	}
	match := e.Exec("sessions", "why", turn, "--session", sid, "--data-dir", e.DataDir)
	match.RequireOK(t)
	if !strings.Contains(match.Stdout, "session "+sid) {
		t.Errorf("--session filter dropped the owning session:\n%s", match.Stdout)
	}
	other := ids[1]
	if other == sid {
		t.Fatal("both runs reused one session")
	}
	mismatch := e.Exec("sessions", "why", turn, "--session", other, "--data-dir", e.DataDir)
	if mismatch.Code != 1 || !strings.Contains(mismatch.Stderr, "not in session") {
		t.Fatalf("mismatched --session = (%d, %q)", mismatch.Code, mismatch.Stderr)
	}
	unknown := e.Exec("sessions", "why", "01JFAKE0000000000000000000", "--data-dir", e.DataDir)
	if unknown.Code != 1 || !strings.Contains(unknown.Stderr, "not found") {
		t.Fatalf("unknown turn = (%d, %q)", unknown.Code, unknown.Stderr)
	}
	if code := e.Exec("sessions", "why", "--data-dir", e.DataDir).Code; code != 2 {
		t.Errorf("why without a turn id = %d, want 2", code)
	}
}

// TestSessionsWhyNoWrite: the command opens the store read-only; the database
// file is byte-identical afterwards.
func TestSessionsWhyNoWrite(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("reply"))
	e.Run("read only why").RequireOK(t)
	sid := e.sessionIDs()[0]
	turn := turnID(t, e.DataDir, sid)
	dbPath := filepath.Join(e.DataDir, "damnbot.db")
	before := fileHash(t, dbPath)
	res := e.Exec("sessions", "why", turn, "--data-dir", e.DataDir)
	res.RequireOK(t)
	if !strings.Contains(res.Stdout, "turn "+turn) {
		t.Fatalf("why output = %q", res.Stdout)
	}
	if after := fileHash(t, dbPath); after != before {
		t.Errorf("sessions why wrote to the database")
	}
}

// TestSessionsWhyCrossSession: a turn of a session created in another working
// directory is still explained without --session.
func TestSessionsWhyCrossSession(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("reply here"), providertest.Text("reply elsewhere"))
	e.Run("here").RequireOK(t)
	here := e.sessionIDs()[0]
	turn := turnID(t, e.DataDir, here)
	res := e.ExecEnv(nil, "run", "-p", "elsewhere", "--model", modelRef, "--base-url", e.Server.URL,
		"--api-key", "e2e", "--offline", "--output", "json", "--data-dir", e.DataDir, "--config", e.cfg)
	res.RequireOK(t)
	why := e.Exec("sessions", "why", turn, "--data-dir", e.DataDir)
	why.RequireOK(t)
	if !strings.Contains(why.Stdout, "session "+here) {
		t.Fatalf("cross-session why = %q", why.Stdout)
	}
}
