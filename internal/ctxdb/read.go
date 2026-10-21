package ctxdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Query is a recall search (SPEC §4.6 Search, §11.6).
type Query struct {
	Text      string // FTS5 query; normalised with dmb_fts_norm (CJK bigrams)
	Limit     int    // k; 0 = DefaultSearchLimit
	SessionID string // optional session filter
}

// DefaultSearchLimit is recall's k when Query.Limit is zero; maxSearchLimit
// bounds a hostile k.
const (
	DefaultSearchLimit = 10
	maxSearchLimit     = 1000
)

// Hit is one recall result: the event or summary a query matched.
type Hit struct {
	EventID   int64   // events.id; for a summary, summaries.event_id
	SessionID string  // owning session
	Seq       int64   // events.seq; for a summary, summaries.to_seq
	Kind      string  // events.kind; "summary_ref" for a summary
	Score     float64 // bm25; lower is better
	Snippet   string  // bounded excerpt around the first matched term
}

// HistoryOptions is the resolved History configuration.
type HistoryOptions struct {
	Limit           int  // max events returned
	Raw             bool // ignore summaries
	IncludeExcluded bool // keep context_exclude turns
}

// HistoryOption adjusts History; History(ctx, sid, fromSeq) stays compatible
// with the SPEC §4.6 signature.
type HistoryOption func(*HistoryOptions)

// DefaultHistoryLimit bounds History when no WithHistoryLimit is given;
// maxHistoryLimit caps an explicit limit.
const (
	DefaultHistoryLimit = 10_000
	maxHistoryLimit     = 100_000
)

// WithHistoryLimit bounds the raw events read (0 = default, capped).
func WithHistoryLimit(n int) HistoryOption { return func(o *HistoryOptions) { o.Limit = n } }

// WithHistoryRaw returns events covered by a summary instead of replacing
// them with one synthetic summary_ref event.
func WithHistoryRaw() HistoryOption { return func(o *HistoryOptions) { o.Raw = true } }

// WithHistoryExcluded keeps turns listed by a context_exclude event
// (GAP-REL-01); default skips them.
func WithHistoryExcluded() HistoryOption { return func(o *HistoryOptions) { o.IncludeExcluded = true } }

func historyOptions(opts []HistoryOption) HistoryOptions {
	o := HistoryOptions{Limit: DefaultHistoryLimit}
	for _, f := range opts {
		f(&o)
	}
	if o.Limit <= 0 {
		o.Limit = DefaultHistoryLimit
	}
	if o.Limit > maxHistoryLimit {
		o.Limit = maxHistoryLimit
	}
	return o
}

// lineageCTE walks fork ancestry (SPEC §4.6, §11.6); path breaks cycles.
const lineageCTE = `WITH RECURSIVE lin(id, lim, depth, path) AS (
	SELECT s.id, 9223372036854775807, 0, ',' || s.id || ','
	  FROM sessions s WHERE s.id = ?1 AND NOT EXISTS (SELECT 1 FROM session_labels vl WHERE vl.session_id = s.id AND vl.key = 'purge_state')
	UNION ALL
	SELECT p.id, coalesce(c.fork_seq, 0), lin.depth + 1, lin.path || p.id || ','
	  FROM lin JOIN sessions c ON c.id = lin.id JOIN sessions p ON p.id = c.parent_id
	 WHERE instr(lin.path, ',' || p.id || ',') = 0 AND NOT EXISTS (SELECT 1 FROM session_labels vl WHERE vl.session_id = p.id AND vl.key = 'purge_state')
)`

// History returns the fork-aware transcript of sessionID (SPEC §4.6):
// ancestors up to their child's fork_seq, then the target's own events.
// fromSeq > 0 is the target-only tail. Summaries replace covered events with
// one summary_ref (unless WithHistoryRaw); context_exclude turns are skipped
// (unless WithHistoryExcluded). A purged or missing session is ErrNotFound.
func (s *Store) History(ctx context.Context, sessionID string, fromSeq int64, opts ...HistoryOption) ([]Event, error) {
	if fromSeq < 0 {
		return nil, fmt.Errorf("ctxdb: history %s from seq %d: %w", sessionID, fromSeq, ErrInvalidSeq)
	}
	o := historyOptions(opts)
	if err := s.Visible(ctx, sessionID); err != nil {
		return nil, err
	}
	var evs []Event
	err := s.each(ctx, lineageCTE+`
		SELECT e.id, e.session_id, e.seq, e.kind, coalesce(e.role, ''), e.content,
		       coalesce(e.turn_id, ''), coalesce(e.tool_call_id, ''), coalesce(e.tool_name, ''),
		       e.payload, coalesce(e.blob_sha, ''), e.created_at
		  FROM lin JOIN events e ON e.session_id = lin.id AND e.seq <= lin.lim
		 WHERE (?2 = 0 OR lin.depth = 0) AND (?2 = 0 OR e.seq > ?2)
		 ORDER BY lin.depth DESC, e.seq LIMIT ?3`, []any{sessionID, fromSeq, o.Limit}, func(r *sql.Rows) error {
		var e Event
		if err := r.Scan(&e.ID, &e.SessionID, &e.Seq, &e.Kind, &e.Role, &e.Content,
			&e.TurnID, &e.ToolCallID, &e.ToolName, &e.Payload, &e.Blob, &e.CreatedAt); err != nil {
			return wrap(err)
		}
		evs = append(evs, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sums, err := s.historySummaries(ctx, sessionID, fromSeq, o.Raw)
	if err != nil {
		return nil, err
	}
	out := applySummaries(evs, sums)
	if !o.IncludeExcluded {
		out = excludeTurns(out)
	}
	if err := s.attachBlobs(ctx, out); err != nil {
		return nil, err
	}
	if len(out) > o.Limit {
		out = out[:o.Limit]
	}
	return out, nil
}

type historySummary struct {
	SessionID string
	ID        int64
	FromSeq   int64
	ToSeq     int64
	Content   string
	CreatedAt int64
}

// historySummaries returns the latest summary per lineage session. fromSeq > 0
// only considers the target session and summaries past the cursor.
func (s *Store) historySummaries(ctx context.Context, sessionID string, fromSeq int64, raw bool) ([]historySummary, error) {
	if raw {
		return nil, nil
	}
	var out []historySummary
	err := s.each(ctx, lineageCTE+`
		SELECT lin.id, sm.id, sm.from_seq, sm.to_seq, sm.content, sm.created_at
		  FROM lin JOIN summaries sm ON sm.session_id = lin.id AND sm.to_seq <= lin.lim
		 WHERE sm.id = (SELECT max(m.id) FROM summaries m
		                 WHERE m.session_id = sm.session_id AND m.to_seq <= lin.lim)
		   AND (?2 = 0 OR (lin.depth = 0 AND sm.to_seq > ?2))
		 ORDER BY lin.depth DESC, sm.id`, []any{sessionID, fromSeq}, func(r *sql.Rows) error {
		var h historySummary
		if err := r.Scan(&h.SessionID, &h.ID, &h.FromSeq, &h.ToSeq, &h.Content, &h.CreatedAt); err != nil {
			return wrap(err)
		}
		out = append(out, h)
		return nil
	})
	return out, err
}

// applySummaries replaces summarized raw events with one summary_ref event per
// session, emitted where that session's segment starts.
func applySummaries(evs []Event, sums []historySummary) []Event {
	if len(sums) == 0 {
		return evs
	}
	bySession := make(map[string]historySummary, len(sums))
	for _, h := range sums {
		bySession[h.SessionID] = h
	}
	emitted := make(map[string]bool, len(sums))
	out := make([]Event, 0, len(evs)+len(sums))
	last := ""
	for i := range evs {
		e := evs[i]
		if e.SessionID != last {
			last = e.SessionID
			if h, ok := bySession[e.SessionID]; ok && !emitted[e.SessionID] {
				out = append(out, summaryEvent(h))
				emitted[e.SessionID] = true
			}
		}
		if h, ok := bySession[e.SessionID]; ok && e.Seq <= h.ToSeq {
			continue
		}
		out = append(out, e)
	}
	for _, h := range sums {
		if !emitted[h.SessionID] {
			out = append(out, summaryEvent(h))
		}
	}
	return out
}

func summaryEvent(h historySummary) Event {
	return Event{
		SessionID: h.SessionID, Seq: h.ToSeq, Kind: "summary_ref", Content: h.Content, CreatedAt: h.CreatedAt,
		Payload: fmt.Sprintf(`{"summary_id":%d,"from_seq":%d,"to_seq":%d}`, h.ID, h.FromSeq, h.ToSeq),
	}
}

type seqSpan struct{ from, to int64 }

func contextExclusions(evs []Event) (map[string]bool, map[string][]seqSpan) {
	turns := map[string]bool{}
	spans := map[string][]seqSpan{}
	for i := range evs {
		if evs[i].Kind != "context_exclude" {
			continue
		}
		var p struct {
			TurnID  string `json:"turn_id"`
			FromSeq int64  `json:"from_seq"`
			ToSeq   int64  `json:"to_seq"`
		}
		if json.Unmarshal([]byte(evs[i].Payload), &p) != nil {
			continue
		}
		if p.TurnID != "" {
			turns[p.TurnID] = true
		}
		if p.FromSeq > 0 || p.ToSeq > 0 {
			spans[evs[i].SessionID] = append(spans[evs[i].SessionID], seqSpan{p.FromSeq, p.ToSeq})
		}
	}
	return turns, spans
}

// excludeTurns drops events of turns marked by context_exclude (GAP-REL-01);
// the marker itself stays for audit.
func excludeTurns(evs []Event) []Event {
	turns, spans := contextExclusions(evs)
	if len(turns) == 0 && len(spans) == 0 {
		return evs
	}
	out := evs[:0]
	for i := range evs {
		e := evs[i]
		drop := e.TurnID != "" && turns[e.TurnID]
		for _, sp := range spans[e.SessionID] {
			drop = drop || e.Seq >= sp.from && (sp.to == 0 || e.Seq <= sp.to)
		}
		if !drop {
			out = append(out, e)
		}
	}
	return out
}

// attachBlobs fills Attach for the returned events (one query per ≤500 ids).
func (s *Store) attachBlobs(ctx context.Context, evs []Event) error {
	if len(evs) == 0 {
		return nil
	}
	byID := make(map[int64]int, len(evs))
	ids := make([]int64, 0, len(evs))
	for i := range evs {
		if evs[i].ID != 0 { // synthetic summary events have no row
			byID[evs[i].ID] = i
			ids = append(ids, evs[i].ID)
		}
	}
	for start := 0; start < len(ids); start += 500 {
		chunk := ids[start:min(start+500, len(ids))]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		q := "SELECT event_id, blob_sha FROM event_attachments WHERE event_id IN (" +
			strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",") + ") ORDER BY event_id, ord"
		if err := s.each(ctx, q, args, func(r *sql.Rows) error {
			var id int64
			var sha string
			if err := r.Scan(&id, &sha); err != nil {
				return wrap(err)
			}
			if i, ok := byID[id]; ok {
				evs[i].Attach = append(evs[i].Attach, sha)
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// Search is `recall`/`sessions search` (SPEC §4.6, §11.6): FTS5 bm25 over
// events and summaries, every candidate filtered through purging(s), at most k
// hits, best first. Rows are scanned directly (no JSON aggregation).
func (s *Store) Search(ctx context.Context, q Query) ([]Hit, error) {
	text := strings.TrimSpace(q.Text)
	if text == "" {
		return nil, nil
	}
	limit := q.Limit
	switch {
	case limit <= 0:
		limit = DefaultSearchLimit
	case limit > maxSearchLimit:
		limit = maxSearchLimit
	}
	expr := searchExpr(text)
	if expr == "" {
		return nil, nil
	}
	hits, err := s.searchFTS(ctx, expr, text, q.SessionID, limit, false)
	if err != nil {
		return nil, err
	}
	sums, err := s.searchFTS(ctx, expr, text, q.SessionID, limit, true)
	if err != nil {
		return nil, err
	}
	hits = append(hits, sums...)
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score < hits[j].Score }) // bm25: lower is better
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func (s *Store) searchFTS(ctx context.Context, expr, text, sid string, limit int, summaries bool) ([]Hit, error) {
	q := `SELECT e.id, e.session_id, e.seq, e.kind, bm25(events_fts), e.content
		FROM events_fts JOIN events e ON e.id = events_fts.rowid
		WHERE events_fts MATCH ? AND ` + notPurging("e.session_id")
	if summaries {
		q = `SELECT sm.event_id, sm.session_id, sm.to_seq, '', bm25(summaries_fts), sm.content
			FROM summaries_fts JOIN summaries sm ON sm.id = summaries_fts.rowid
			WHERE summaries_fts MATCH ? AND ` + notPurging("sm.session_id")
	}
	args := []any{expr}
	if sid != "" {
		q += " AND session_id = ?"
		args = append(args, sid)
	}
	order := "bm25(events_fts)"
	if summaries {
		order = "bm25(summaries_fts)"
	}
	q += " ORDER BY " + order + " LIMIT ?"
	args = append(args, limit)
	var out []Hit
	err := s.each(ctx, q, args, func(r *sql.Rows) error {
		var h Hit
		var content string
		if err := r.Scan(&h.EventID, &h.SessionID, &h.Seq, &h.Kind, &h.Score, &content); err != nil {
			return wrap(err)
		}
		if summaries {
			h.Kind = "summary_ref"
		}
		h.Snippet = snippet(content, text)
		out = append(out, h)
		return nil
	})
	return out, err
}

// searchExpr builds Search's syntax-safe FTS5 expression (Recall uses ftsQuery):
// dmb_fts_norm tokens, each quoted; CJK bigrams OR-ed, a one-rune CJK token is
// a prefix query, and separate words are AND-ed.
func searchExpr(text string) string {
	var parts, cjk []string
	flush := func() {
		if len(cjk) > 0 {
			parts = append(parts, "("+strings.Join(cjk, " OR ")+")")
			cjk = nil
		}
	}
	for tok := range strings.FieldsSeq(ftsNorm(text)) {
		tok = strings.ReplaceAll(tok, `"`, `""`)
		if tok == "" {
			continue
		}
		quoted := `"` + tok + `"`
		if !hasCJK(tok) {
			flush()
			parts = append(parts, quoted)
			continue
		}
		if utf8.RuneCountInString(tok) == 1 {
			quoted += "*"
		}
		cjk = append(cjk, quoted)
	}
	flush()
	return strings.Join(parts, " AND ")
}

func hasCJK(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
			return true
		}
	}
	return false
}

const snippetRunes = 160

// snippet returns a one-line excerpt around the first term hit. FTS5
// contentless tables cannot call snippet(), so it is built from the row.
func snippet(content, query string) string {
	text := strings.Join(strings.Fields(content), " ")
	if utf8.RuneCountInString(text) <= snippetRunes {
		return text
	}
	lower := strings.ToLower(text)
	best := -1
	for term := range strings.FieldsSeq(strings.ToLower(query)) {
		term = strings.TrimFunc(term, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if term == "" {
			continue
		}
		if i := strings.Index(lower, term); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	runes := []rune(text)
	start := 0
	if best >= 0 {
		start = utf8.RuneCountInString(text[:best]) - snippetRunes/3
	}
	start = max(start, 0)
	end := min(start+snippetRunes, len(runes))
	if end-start < 1 {
		return text
	}
	return string(runes[start:end])
}

// Fork creates a child at atSeq (SPEC §4.6): parent_id/fork_seq, no copied
// events, child lease in the same transaction. Rewind = an earlier atSeq; the
// old branch stays. atSeq outside [0, head] is ErrInvalidSeq.
func (s *Store) Fork(ctx context.Context, sessionID string, atSeq int64) (string, error) {
	if atSeq < 0 {
		return "", fmt.Errorf("ctxdb: fork %s at seq %d: %w", sessionID, atSeq, ErrInvalidSeq)
	}
	src, err := s.forkSource(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if atSeq > src.head {
		return "", fmt.Errorf("ctxdb: fork %s at seq %d > head %d: %w", sessionID, atSeq, src.head, ErrInvalidSeq)
	}
	id := newID()
	err = s.Write(ctx, func(t *Tx) error {
		_, cerr := t.CreateSession(Session{
			ID: id, ParentID: sessionID, ForkSeq: atSeq, Origin: src.origin, Cwd: src.cwd,
			Model: src.model, ConfigSHA: src.configSHA, ContextTier: src.contextTier,
		})
		return cerr
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

type forkSource struct {
	origin, cwd, model, configSHA, contextTier string
	head                                       int64
}

func (s *Store) forkSource(ctx context.Context, sid string) (forkSource, error) {
	var f forkSource
	var labelled bool
	err := s.get(ctx, `SELECT origin, cwd, model, config_sha, context_tier,
		coalesce((SELECT max(seq) FROM events WHERE session_id = s.id), 0),
		EXISTS (SELECT 1 FROM session_labels l WHERE l.session_id = s.id AND l.key = 'purge_state')
		FROM sessions s WHERE s.id = ?1`, []any{sid},
		&f.origin, &f.cwd, &f.model, &f.configSHA, &f.contextTier, &f.head, &labelled)
	if err != nil {
		return f, err
	}
	if labelled {
		return f, fmt.Errorf("ctxdb: session %s: %w", sid, ErrSessionPurging)
	}
	return f, nil
}
