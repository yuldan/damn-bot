package e2e

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// sessionIDs returns the ids printed by `sessions ls` (first column).
func (e *Env) sessionIDs() []string {
	res := e.Exec("sessions", "ls", "--data-dir", e.DataDir)
	res.RequireOK(e.t)
	var ids []string
	for line := range strings.Lines(strings.TrimSpace(res.Stdout)) {
		if id, _, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok && id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// TestSessionsSearch: FTS recall through the real binary, incl. scope and
// limit; the empty query is a usage error.
func TestSessionsSearch(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("noted"), providertest.Text("other"))
	e.Run("remember the zebra token").RequireOK(t)
	e.Run("unrelated turn").RequireOK(t)
	ids := e.sessionIDs()
	if len(ids) != 2 {
		t.Fatalf("sessions ls = %v, want 2 ids", ids)
	}
	res := e.Exec("sessions", "search", "zebra", "--data-dir", e.DataDir)
	res.RequireOK(t)
	if strings.Count(strings.TrimSpace(res.Stdout), "\n") != 0 {
		t.Fatalf("search zebra returned more than one row:\n%s", res.Stdout)
	}
	fields := strings.Split(strings.TrimSpace(res.Stdout), "\t")
	if len(fields) != 4 || fields[0] == "" || fields[2] != "user_message" || !strings.Contains(fields[3], "zebra") {
		t.Fatalf("search row = %q", res.Stdout)
	}
	other := ids[0]
	if fields[0] == other {
		other = ids[1]
	}
	filtered := e.Exec("sessions", "search", "zebra", "--session", other, "--data-dir", e.DataDir)
	filtered.RequireOK(t)
	if strings.TrimSpace(filtered.Stdout) != "" {
		t.Fatalf("search filtered to an unrelated session returned:\n%s", filtered.Stdout)
	}
	limited := e.Exec("sessions", "search", "zebra", "--limit", "1", "--data-dir", e.DataDir)
	limited.RequireOK(t)
	if strings.Count(strings.TrimSpace(limited.Stdout), "\n") != 0 {
		t.Fatalf("--limit 1 returned more than one row:\n%s", limited.Stdout)
	}
	if code := e.Exec("sessions", "search", "--data-dir", e.DataDir).Code; code != 2 {
		t.Errorf("empty query exit = %d, want 2", code)
	}
}

// TestSessionsPruneID: one session is purged, the other survives; --yes is
// mandatory and --dry-run deletes nothing.
func TestSessionsPruneID(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("one"), providertest.Text("two"))
	e.Run("keep this session").RequireOK(t)
	e.Run("delete that session").RequireOK(t)
	ids := e.sessionIDs()
	if len(ids) != 2 {
		t.Fatalf("sessions ls = %v, want 2 ids", ids)
	}
	del := e.Exec("sessions", "search", "delete that session", "--data-dir", e.DataDir)
	del.RequireOK(t)
	target, _, _ := strings.Cut(del.Stdout, "\t")
	if target == "" {
		t.Fatalf("search did not find the target session")
	}
	keep := ids[0]
	if keep == target {
		keep = ids[1]
	}
	preview := e.Exec("sessions", "prune", target, "--dry-run", "--data-dir", e.DataDir)
	preview.RequireOK(t)
	if !strings.Contains(preview.Stdout, "would purge 1 sessions") || !strings.Contains(preview.Stdout, target) {
		t.Fatalf("dry-run = %q", preview.Stdout)
	}
	if got := len(e.sessionIDs()); got != 2 {
		t.Fatalf("dry-run deleted a session: %d left", got)
	}
	if code := e.Exec("sessions", "prune", target, "--data-dir", e.DataDir).Code; code != 2 {
		t.Fatalf("prune without --yes = %d, want 2", code)
	}
	done := e.Exec("sessions", "prune", target, "--yes", "--data-dir", e.DataDir)
	done.RequireOK(t)
	if !strings.Contains(done.Stdout, "purged 1 sessions") {
		t.Fatalf("prune = %q", done.Stdout)
	}
	left := e.sessionIDs()
	if len(left) != 1 || left[0] != keep {
		t.Fatalf("sessions after prune = %v, want [%s]", left, keep)
	}
	if res := e.Exec("sessions", "show", target, "--data-dir", e.DataDir); res.Code == 0 {
		t.Errorf("show of the purged session still succeeds:\n%s", res.Stdout)
	}
	if code := e.Exec("sessions", "prune", target, "--yes", "--data-dir", e.DataDir).Code; code != 1 {
		t.Errorf("prune of an unknown id = %d, want 1", code)
	}
}

// TestSessionsPruneForks: a session with descendants is refused (has_forks)
// unless --with-forks takes the whole lineage.
func TestSessionsPruneForks(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("parent reply"))
	e.Run("parent session").RequireOK(t)
	ids := e.sessionIDs()
	if len(ids) != 1 {
		t.Fatalf("sessions ls = %v, want 1 id", ids)
	}
	parent := ids[0]
	e.Exec("sessions", "fork", parent, "--data-dir", e.DataDir).RequireOK(t)
	if got := len(e.sessionIDs()); got != 2 {
		t.Fatalf("sessions after fork = %d, want 2", got)
	}
	refused := e.Exec("sessions", "prune", parent, "--yes", "--data-dir", e.DataDir)
	if refused.Code != 1 || !strings.Contains(refused.Stderr, "has forks") {
		t.Fatalf("prune parent = (%d, %q), want has_forks refusal", refused.Code, refused.Stderr)
	}
	if got := len(e.sessionIDs()); got != 2 {
		t.Fatalf("refused prune changed the store: %d sessions", got)
	}
	done := e.Exec("sessions", "prune", parent, "--with-forks", "--yes", "--data-dir", e.DataDir)
	done.RequireOK(t)
	if !strings.Contains(done.Stdout, "purged 2 sessions") {
		t.Fatalf("prune --with-forks = %q", done.Stdout)
	}
	if got := len(e.sessionIDs()); got != 0 {
		t.Fatalf("sessions after lineage purge = %d, want 0", got)
	}
}
