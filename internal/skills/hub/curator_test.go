package hub

import (
	"testing"
	"time"
)

func curatorReport() (UsageReport, time.Time) {
	now := time.Unix(100*24*3600, 0)
	stale := now.Add(-60 * 24 * time.Hour).Unix()
	return UsageReport{Total: 4, Entries: []UsageEntry{
		{Name: "alpha", Indexed: 1, Approved: 1, LastAt: stale},
		{Name: "brag", Indexed: 1, LastAt: stale},
		{Name: "brag-slim", Indexed: 1, LastAt: stale},
		{Name: "fresh", Indexed: 1, LastAt: now.Add(-time.Hour).Unix()},
		{Name: "old", Quarantined: 1, LastAt: stale},
	}}, now
}

func TestF8SKILL021Curator(t *testing.T) {
	t.Parallel()
	rep, now := curatorReport()
	cfg := CuratorConfig{Now: func() time.Time { return now }, Budget: 50}
	// Less than two hours idle: nothing runs, even with stale entries.
	if got := Curate(rep, now.Add(-time.Hour), cfg); len(got) != 0 {
		t.Fatalf("idle 1h proposals = %v", got)
	}
	got := Curate(rep, now.Add(-3*time.Hour), cfg)
	want := map[string]string{"alpha": "archive", "brag-slim": "merge", "old": "retire"}
	if len(got) != len(want) {
		t.Fatalf("proposals = %+v, want %v", got, want)
	}
	for _, p := range got {
		if want[p.Name] != p.Action {
			t.Errorf("%s action = %q, want %q", p.Name, p.Action, want[p.Name])
		}
		if p.Reason == "" {
			t.Errorf("%s has an empty reason", p.Name)
		}
	}
}

func TestF8CuratorBudget(t *testing.T) {
	t.Parallel()
	rep, now := curatorReport()
	base := CuratorConfig{Now: func() time.Time { return now }, MaxActions: 50, Budget: 50}
	if got := Curate(rep, now.Add(-3*time.Hour), base); len(got) != 3 {
		t.Fatalf("unlimited proposals = %d, want 3", len(got))
	}
	limited := base
	limited.Budget = 2
	if got := Curate(rep, now.Add(-3*time.Hour), limited); len(got) != 2 {
		t.Fatalf("budget 2 proposals = %d, want 2", len(got))
	}
	zero := base
	zero.Budget = 0
	if got := Curate(rep, now.Add(-3*time.Hour), zero); len(got) != 0 {
		t.Fatalf("budget 0 proposals = %v, want none", got)
	}
	capped := base
	capped.Budget = 50
	capped.MaxActions = 1
	if got := Curate(rep, now.Add(-3*time.Hour), capped); len(got) != 1 {
		t.Fatalf("MaxActions 1 proposals = %d, want 1", len(got))
	}
}
