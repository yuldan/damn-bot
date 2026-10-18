package ctxdb

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
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

// Search is `recall`/`sessions search`: FTS match filtered per candidate.
func (s *Store) Search(ctx context.Context, q string) ([]string, error) {
	return s.strings(ctx, `SELECT e.session_id FROM events_fts f JOIN events e ON e.id = f.rowid
		WHERE events_fts MATCH dmb_fts_norm(?) AND `+notPurging("e.session_id")+` ORDER BY bm25(events_fts)`, q)
}

type sessNode struct {
	parent   string
	activity int64 // created_at of the max-seq event, else sessions.created_at
	labelled bool
}

// sessForest is the session tree used to compute P.
type sessForest struct {
	nodes map[string]sessNode
	kids  map[string][]string
}

// sub returns id and all its descendants.
func (f *sessForest) sub(id string) []string {
	out := []string{id}
	for _, k := range f.kids[id] {
		out = append(out, f.sub(k)...)
	}
	return out
}

func (f *sessForest) depth(id string) int {
	d := 0
	for p := f.nodes[id].parent; p != ""; p = f.nodes[p].parent {
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
		if err := wrap(r.Scan(&id, &n.parent, &n.activity, &n.labelled)); err != nil {
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
	slices.SortStableFunc(rep.P, func(a, b string) int { return f.depth(b) - f.depth(a) })
	return rep, nil
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
