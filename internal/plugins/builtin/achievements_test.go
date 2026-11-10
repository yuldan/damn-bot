package builtin

import (
	"errors"
	"testing"
	"time"
)

// TestF8PLUG019Achievements: achievements are local events, unlocked once,
// bounded and recorded without any provider call path (the package has no
// provider client).
func TestF8PLUG019Achievements(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	a := NewAchievements(func() time.Time { return now })
	ev, ok, err := a.Record("first_turn")
	if err != nil || !ok || ev.ID != "first_turn" || !ev.At.Equal(now) {
		t.Fatalf("Record = (%+v, %t, %v)", ev, ok, err)
	}
	if _, ok, err := a.Record("first_turn"); err != nil || ok {
		t.Fatalf("duplicate Record = (%t, %v)", ok, err)
	}
	if _, _, err := a.Record("foreign-achievement"); !errors.Is(err, ErrUnknownAchievement) {
		t.Fatalf("unknown Record err = %v, want ErrUnknownAchievement", err)
	}
	list := a.List()
	if len(list) != 1 {
		t.Fatalf("List = %d entries, want 1", len(list))
	}
	list[0].Title = "mutated"
	if again := a.List(); again[0].Title == "mutated" {
		t.Fatal("List returned a view into the recorder")
	}
	if _, err := Resolve(Targets{}, AchievementsID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unwired target err = %v, want ErrUnavailable", err)
	}
	if _, err := Resolve(Targets{Achievements: true}, AchievementsID); err != nil {
		t.Fatalf("wired target: %v", err)
	}
	for range 150 {
		if _, _, err := a.Record("first_tool"); err != nil {
			t.Fatalf("duplicate Record: %v", err)
		}
	}
	if len(a.List()) != 2 {
		t.Fatalf("recorder accepted duplicate/full events: %d", len(a.List()))
	}
}
