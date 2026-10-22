package e2e

import (
	"fmt"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// TestSessionsReadOnlyNewerSchema (acceptance 9): a newer-schema DB is
// readable through the read-only inspection command, while the writer exits
// with the SPEC §4.3 schema_newer code (1) and makes no provider call.
func TestSessionsReadOnlyNewerSchema(t *testing.T) {
	t.Parallel()
	e := New(t)
	ctx := t.Context()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: e.DataDir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: "newer", Origin: "headless", Cwd: "/w", Model: "openai/gpt-fake"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Append(ctx, "newer", nil, ctxdb.Event{Kind: "user_message", Role: "user", TurnID: "t", Content: "old row"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		if _, xerr := tx.Exec("PRAGMA user_version = 9999", nil); xerr != nil {
			return fmt.Errorf("set user_version: %w", xerr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	show := e.Exec("sessions", "show", "newer", "--data-dir", e.DataDir)
	if show.Code != 0 {
		t.Fatalf("sessions show exit = %d\nstdout:\n%s\nstderr:\n%s", show.Code, show.Stdout, show.Stderr)
	}
	for _, want := range []string{"id: newer", "events: 1"} {
		if !strings.Contains(show.Stdout, want) {
			t.Errorf("show output lacks %q: %s", want, show.Stdout)
		}
	}
	ls := e.Exec("sessions", "ls", "--data-dir", e.DataDir)
	if ls.Code != 0 || !strings.Contains(ls.Stdout, "newer") {
		t.Fatalf("sessions ls = %d %q (stderr %s)", ls.Code, ls.Stdout, ls.Stderr)
	}
	run := e.Run("probe")
	if run.Code != 1 {
		t.Fatalf("writer exit = %d, want 1\nstdout:\n%s\nstderr:\n%s", run.Code, run.Stdout, run.Stderr)
	}
	if !strings.Contains(run.Stderr, "schema_newer") {
		t.Errorf("writer stderr lacks schema_newer: %s", run.Stderr)
	}
	if got := len(e.Requests()); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
}
