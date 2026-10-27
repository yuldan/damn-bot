package ctxdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestTurnSessions: a turn id is not globally unique, so every owning session
// is returned; unknown and empty ids are ErrNotFound.
func TestTurnSessions(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	ctx := context.Background()
	session(t, s, "S1", "")
	session(t, s, "S2", "")
	must(t, s.Append2(ctx, "S1",
		Event{Kind: "turn_start", TurnID: "T1"},
		Event{Kind: "turn_end", TurnID: "T1"}))
	must(t, s.Append2(ctx, "S2", Event{Kind: "turn_start", TurnID: "T1"}))
	must(t, s.Append2(ctx, "S2", Event{Kind: "turn_start", TurnID: "T2"}))
	got, err := s.TurnSessions(ctx, "T1")
	must(t, err)
	if !slices.Equal(got, []string{"S1", "S2"}) {
		t.Fatalf("TurnSessions(T1) = %v", got)
	}
	got, err = s.TurnSessions(ctx, "T2")
	must(t, err)
	if !slices.Equal(got, []string{"S2"}) {
		t.Fatalf("TurnSessions(T2) = %v", got)
	}
	if _, err = s.TurnSessions(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown turn error = %v, want ErrNotFound", err)
	}
	if _, err = s.TurnSessions(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty turn error = %v, want ErrNotFound", err)
	}
}

// TestTurnSessionsUsesIndex (migration 0013): the global turn lookup must use
// idx_events_turn_id; the composite (session_id, turn_id) index cannot.
func TestTurnSessionsUsesIndex(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	ctx := context.Background()
	var plan []string
	q := `EXPLAIN QUERY PLAN SELECT DISTINCT e.session_id FROM events e
		WHERE e.turn_id = ?1 AND ` + notPurging("e.session_id") + `
		ORDER BY e.session_id`
	err := s.each(ctx, q, []any{"T1"}, func(r *sql.Rows) error {
		var id, parent, unused int
		var detail string
		if err := r.Scan(&id, &parent, &unused, &detail); err != nil {
			return fmt.Errorf("scan query plan: %w", err)
		}
		plan = append(plan, detail)
		return nil
	})
	must(t, err)
	if !strings.Contains(strings.Join(plan, "\n"), "idx_events_turn_id") {
		t.Fatalf("query plan does not use idx_events_turn_id:\n%s", strings.Join(plan, "\n"))
	}
}
