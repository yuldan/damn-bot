package ctxdb

import (
	"context"
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
	if err := s.get(ctx, "SELECT count(*) FROM sessions s WHERE s.id = ? AND "+notPurging("s.id"), []any{sid}, &n); err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("ctxdb: session %s: %w", sid, ErrNotFound)
	}
	return nil
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

func (s *Store) strings(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

type sessNode struct {
	parent   string
	activity int64 // created_at of the max-seq event, else sessions.created_at
	labelled bool
}

// purgeSet computes P (descendants first) per §11.6 "Himpunan purge P".
func (s *Store) purgeSet(ctx context.Context, req PurgeRequest) (PurgeReport, error) {
	var rep PurgeReport
	rows, err := s.r.QueryContext(ctx, `SELECT s.id, coalesce(s.parent_id, ''),
		coalesce((SELECT created_at FROM events e WHERE e.session_id = s.id ORDER BY seq DESC LIMIT 1), s.created_at),
		NOT `+notPurging("s.id")+` FROM sessions s WHERE s.id <> '__system__'`)
	if err != nil {
		return rep, classify(err)
	}
	nodes := map[string]sessNode{}
	kids := map[string][]string{}
	for rows.Next() {
		var id string
		var n sessNode
		if err := rows.Scan(&id, &n.parent, &n.activity, &n.labelled); err != nil {
			_ = rows.Close()
			return rep, err
		}
		nodes[id] = n
		kids[n.parent] = append(kids[n.parent], id)
	}
	if err := rows.Close(); err != nil {
		return rep, err
	}
	var sub func(string) []string // id + all descendants
	sub = func(id string) []string {
		out := []string{id}
		for _, k := range kids[id] {
			out = append(out, sub(k)...)
		}
		return out
	}
	if req.OlderThan > 0 {
		rep.P, rep.Skipped = olderThan(nodes, kids, sub, s.clk.WallMs()-req.OlderThan.Milliseconds(), req.WithForks)
	}
	for _, id := range req.IDs {
		n, ok := nodes[id]
		switch {
		case id == SystemSession:
			return rep, fmt.Errorf("ctxdb: %s cannot be purged: %w", id, ErrInternal)
		case !ok:
			return rep, fmt.Errorf("ctxdb: session %s: %w", id, ErrNotFound)
		case n.labelled:
			return rep, fmt.Errorf("ctxdb: session %s: %w", id, ErrSessionPurging)
		case len(kids[id]) > 0 && !req.WithForks:
			return rep, fmt.Errorf("ctxdb: session %s has forks %v: %w", id, sub(id)[1:], ErrHasForks)
		}
		rep.P = append(rep.P, sub(id)...)
	}
	slices.Sort(rep.P)
	rep.P = slices.Compact(rep.P)
	depth := func(id string) int {
		d := 0
		for p := nodes[id].parent; p != ""; p = nodes[p].parent {
			d++
		}
		return d
	}
	slices.SortStableFunc(rep.P, func(a, b string) int { return depth(b) - depth(a) })
	return rep, nil
}

// olderThan: without forks only childless sessions older than cutoff (the
// others are reported has_forks); with forks only maximal subtrees whose
// every member is older than cutoff.
func olderThan(nodes map[string]sessNode, kids map[string][]string, sub func(string) []string, cutoff int64, withForks bool) (p, skipped []string) {
	newest := func(id string) int64 {
		m := int64(0)
		for _, x := range sub(id) {
			m = max(m, nodes[x].activity)
		}
		return m
	}
	for id, n := range nodes {
		if n.labelled || n.activity >= cutoff {
			continue
		}
		switch {
		case !withForks && len(kids[id]) > 0:
			skipped = append(skipped, id)
		case !withForks:
			p = append(p, id)
		case newest(id) < cutoff && (n.parent == "" || nodes[n.parent].labelled || newest(n.parent) >= cutoff):
			p = append(p, sub(id)...)
		}
	}
	return p, skipped
}
