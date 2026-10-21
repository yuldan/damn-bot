package ctxdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// notPurging(col) is the purging(s) filter of §11.6, O(log n) per session
// through idx_session_labels. Every read path over sessions uses it.
func notPurging(col string) string {
	return "NOT EXISTS (SELECT 1 FROM session_labels vl WHERE vl.session_id = " + col + " AND vl.key = 'purge_state')"
}

// Visible is ctxdb.Visible: a missing or labelled session is not_found.
func (s *Store) Visible(ctx context.Context, sid string) error {
	var n int
	err := s.get(ctx, "SELECT count(*) FROM sessions s WHERE s.id = ? AND "+notPurging("s.id"), []any{sid}, &n)
	if err == nil && n == 0 {
		err = fmt.Errorf("ctxdb: session %s: %w", sid, ErrNotFound)
	}
	return err
}

// ListSessions is `sessions ls` (visible sessions only, newest first).
func (s *Store) ListSessions(ctx context.Context) ([]string, error) {
	return s.strings(ctx, "SELECT id FROM sessions s WHERE id <> '__system__' AND "+notPurging("s.id")+" ORDER BY created_at DESC, id")
}

// Search and History live in read.go.

// RecallHit is one matching event (SPEC §11.6 `recall`).
type RecallHit struct {
	SessionID string
	EventID   int64
}

// ftsQuery turns free text into a conservative FTS5 expression: runs of
// letters/digits become terms and everything else is a separator. CJK runs
// stay contiguous so dmb_fts_norm can expand them to bigrams.
func ftsQuery(text string) string {
	var b strings.Builder
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteByte(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// broadMatches bounds exact bm25 ranking: above it a query would score every
// matching event. Broad terms fall back to newest-first candidates.
const broadMatches = 15000

// Recall is the `recall` query of SPEC §11.6: the k best visible event
// matches of text. Visibility (the purging label) is applied inside SQL before
// ORDER BY/LIMIT, so only visible candidates are ranked: no OFFSET paging, no
// quadratic rescan, and a purged prefix cannot starve k (JN3-12/JN3-14). A
// broad term (probe above [broadMatches]) uses newest-first order instead of
// scoring every match. Free text is tokenised (see ftsQuery), so
// quotes/parentheses/column filters cannot become a MATCH syntax error.
func (s *Store) Recall(ctx context.Context, text string, k int) ([]RecallHit, error) {
	return s.recall(ctx, text, k, broadMatches)
}

// recall is Recall with the broad threshold as a parameter, so tests reach
// both sides of it without appending broadMatches events.
func (s *Store) recall(ctx context.Context, text string, k, broadAt int) ([]RecallHit, error) {
	if k <= 0 {
		k = 10
	}
	q := ftsQuery(text)
	if q == "" {
		return nil, nil
	}
	probe, err := s.ints(ctx, `SELECT rowid FROM events_fts
		WHERE events_fts MATCH dmb_fts_norm(?, 'events.content', '') LIMIT ?`, q, broadAt+1)
	if err != nil {
		return nil, fmt.Errorf("ctxdb: recall probe: %w", err)
	}
	broad := len(probe) > broadAt
	purged, err := s.Count(ctx, `SELECT count(*) FROM session_labels WHERE key = 'purge_state'`)
	if err != nil {
		return nil, fmt.Errorf("ctxdb: recall purge check: %w", err)
	}
	from := "events_fts f"
	where := "events_fts MATCH dmb_fts_norm(?, 'events.content', '')"
	args := []any{q}
	if purged > 0 {
		// Materialize the small visible-rowid set once; FTS then probes it,
		// instead of doing an events+labels lookup for every purged match
		// (JN3-14).
		where += ` AND f.rowid IN (SELECT e2.id FROM events e2 WHERE e2.session_id NOT IN
			(SELECT vl.session_id FROM session_labels vl WHERE vl.key = 'purge_state'))`
	}
	order := "bm25(events_fts)"
	if broad {
		order = "f.rowid DESC"
	}
	args = append(args, k)
	ids, err := s.ints(ctx, "SELECT f.rowid FROM "+from+" WHERE "+where+" ORDER BY "+order+" LIMIT ?", args...)
	if err != nil {
		return nil, fmt.Errorf("ctxdb: recall query: %w", err)
	}
	out := make([]RecallHit, 0, len(ids))
	for _, id := range ids {
		var h RecallHit
		gerr := s.get(ctx, `SELECT e.id, e.session_id FROM events e WHERE e.id = ? AND `+notPurging("e.session_id"),
			[]any{id}, &h.EventID, &h.SessionID)
		if errors.Is(gerr, ErrNotFound) {
			continue
		}
		if gerr != nil {
			return nil, fmt.Errorf("ctxdb: recall hit: %w", gerr)
		}
		out = append(out, h)
	}
	return out, nil
}

type sessNode struct {
	parent   string
	activity int64 // created_at of the max-seq event, else sessions.created_at
	labelled bool
	pinned   bool // latest pinned label is 1: never purged (nor its ancestors)
}

// sessForest is the session tree used to compute P.
type sessForest struct {
	nodes map[string]sessNode
	kids  map[string][]string
}

// sub returns id and all its descendants. It walks without a depth limit
// (EC-STATE-014) but with a visited set, so a corrupt parent_id cycle cannot
// recurse forever.
func (f *sessForest) sub(id string) []string {
	out := make([]string, 0, 1)
	seen := map[string]bool{}
	stack := []string{id}
	for len(stack) > 0 {
		x := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[x] {
			continue
		}
		seen[x] = true
		out = append(out, x)
		for _, k := range slices.Backward(f.kids[x]) {
			if !seen[k] {
				stack = append(stack, k)
			}
		}
	}
	return out
}

func (f *sessForest) depth(id string) int {
	d := 0
	seen := map[string]bool{id: true}
	for p := f.nodes[id].parent; p != ""; p = f.nodes[p].parent {
		if seen[p] {
			break
		}
		seen[p] = true
		d++
	}
	return d
}

// purgeSet computes P (descendants first) per §11.6 "Himpunan purge P".
func (s *Store) purgeSet(ctx context.Context, req PurgeRequest) (PurgeReport, error) {
	var rep PurgeReport
	f := &sessForest{nodes: map[string]sessNode{}, kids: map[string][]string{}}
	err := s.each(ctx, s.q["purge.nodes"], nil, func(r *sql.Rows) error {
		var id string
		var n sessNode
		if err := wrap(r.Scan(&id, &n.parent, &n.activity, &n.labelled, &n.pinned)); err != nil {
			return err
		}
		f.nodes[id] = n
		f.kids[n.parent] = append(f.kids[n.parent], id)
		return nil
	})
	if err != nil {
		return rep, err
	}
	if req.OlderThan > 0 {
		rep.P, rep.Skipped = f.olderThan(s.clk.WallMs()-req.OlderThan.Milliseconds(), req.WithForks)
	}
	for _, id := range req.IDs {
		n, ok := f.nodes[id]
		switch {
		case id == SystemSession:
			return rep, fmt.Errorf("ctxdb: %s cannot be purged: %w", id, ErrInternal)
		case !ok:
			return rep, fmt.Errorf("ctxdb: session %s: %w", id, ErrNotFound)
		case n.labelled:
			return rep, fmt.Errorf("ctxdb: session %s: %w", id, ErrSessionPurging)
		case len(f.kids[id]) > 0 && !req.WithForks:
			return rep, fmt.Errorf("ctxdb: session %s has forks %v: %w", id, f.sub(id)[1:], ErrHasForks)
		}
		rep.P = append(rep.P, f.sub(id)...)
	}
	slices.Sort(rep.P)
	rep.P = slices.Compact(rep.P)
	rep.P, rep.Pinned = f.dropPinned(rep.P)
	slices.SortStableFunc(rep.P, func(a, b string) int { return f.depth(b) - f.depth(a) })
	return rep, nil
}

// dropPinned removes pinned sessions from P together with their ancestors:
// deleting an ancestor would take the pinned row with it (and break the
// parent FK). Pinned roots removed from P are reported so the CLI can explain
// the skip.
func (f *sessForest) dropPinned(p []string) (kept, pinned []string) {
	drop := map[string]bool{}
	for _, id := range p {
		if !f.nodes[id].pinned {
			continue
		}
		pinned = append(pinned, id)
		for cur := id; cur != ""; cur = f.nodes[cur].parent {
			drop[cur] = true
		}
	}
	kept = p[:0]
	for _, id := range p {
		if !drop[id] {
			kept = append(kept, id)
		}
	}
	slices.Sort(pinned)
	return kept, pinned
}

// olderThan: without forks only childless sessions older than cutoff (the
// others are reported has_forks); with forks only maximal subtrees whose
// every member is older than cutoff.
func (f *sessForest) olderThan(cutoff int64, withForks bool) (p, skipped []string) {
	newest := func(id string) int64 {
		m := int64(0)
		for _, x := range f.sub(id) {
			m = max(m, f.nodes[x].activity)
		}
		return m
	}
	for id, n := range f.nodes {
		switch {
		case n.labelled || n.activity >= cutoff:
		case !withForks && len(f.kids[id]) > 0:
			skipped = append(skipped, id)
		case !withForks:
			p = append(p, id)
		case newest(id) < cutoff && (n.parent == "" || f.nodes[n.parent].labelled || newest(n.parent) >= cutoff):
			p = append(p, f.sub(id)...)
		}
	}
	return p, skipped
}
