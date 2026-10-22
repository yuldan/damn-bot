package ctxdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	Excluded  bool    // the hit belongs to a context_exclude turn (SEC11-5)
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
	turns := map[string]bool{}
	if !o.IncludeExcluded {
		turns = contextExclusions(evs)
		sums = dropExcludedSummaries(sums, evs, turns) // SEC11-1: before summaries
	}
	out := applySummaries(evs, sums)
	if !o.IncludeExcluded {
		out = excludeTurns(out, turns)
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

// contextExclusions returns the turn ids marked by context_exclude. Span
// markers (from_seq/to_seq) are intentionally ignored (SEC11-4): no writer
// emits them, and a marker with only from_seq would hide every later turn.
func contextExclusions(evs []Event) map[string]bool {
	turns := map[string]bool{}
	for i := range evs {
		if evs[i].Kind != "context_exclude" {
			continue
		}
		var p struct {
			TurnID string `json:"turn_id"`
		}
		if json.Unmarshal([]byte(evs[i].Payload), &p) != nil || p.TurnID == "" {
			continue
		}
		turns[p.TurnID] = true
	}
	return turns
}

// dropExcludedSummaries drops a summary whose covered range contains an
// excluded turn's events (SEC11-1): compaction can run inside a turn that
// later fails, and the summary would otherwise re-introduce that turn.
func dropExcludedSummaries(sums []historySummary, evs []Event, turns map[string]bool) []historySummary {
	if len(turns) == 0 || len(sums) == 0 {
		return sums
	}
	ranges := map[string][2]int64{}
	for i := range evs {
		e := &evs[i]
		if e.TurnID == "" || !turns[e.TurnID] {
			continue
		}
		r, ok := ranges[e.SessionID]
		if !ok || e.Seq < r[0] {
			r[0] = e.Seq
		}
		if !ok || e.Seq > r[1] {
			r[1] = e.Seq
		}
		ranges[e.SessionID] = r
	}
	out := sums[:0]
	for _, h := range sums {
		r, bad := ranges[h.SessionID]
		if bad && h.FromSeq <= r[1] && h.ToSeq >= r[0] {
			continue
		}
		out = append(out, h)
	}
	return out
}

// excludeTurns drops events of turns marked by context_exclude (GAP-REL-01);
// the marker itself stays for audit.
func excludeTurns(evs []Event, turns map[string]bool) []Event {
	if len(turns) == 0 {
		return evs
	}
	out := evs[:0]
	for i := range evs {
		if e := evs[i]; e.TurnID == "" || !turns[e.TurnID] {
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
	q := `SELECT e.id, e.session_id, e.seq, e.kind, bm25(events_fts), e.content,
		EXISTS (SELECT 1 FROM events x WHERE x.session_id = e.session_id AND x.kind = 'context_exclude'
			AND json_extract(x.payload, '$.turn_id') = e.turn_id)
		FROM events_fts JOIN events e ON e.id = events_fts.rowid
		WHERE events_fts MATCH ? AND ` + notPurging("e.session_id")
	if summaries {
		q = `SELECT sm.event_id, sm.session_id, sm.to_seq, '', bm25(summaries_fts), sm.content,
			EXISTS (SELECT 1 FROM events x JOIN events t ON t.session_id = x.session_id
				WHERE x.session_id = sm.session_id AND x.kind = 'context_exclude'
				AND json_extract(x.payload, '$.turn_id') = t.turn_id AND t.seq BETWEEN sm.from_seq AND sm.to_seq)
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
		if err := r.Scan(&h.EventID, &h.SessionID, &h.Seq, &h.Kind, &h.Score, &content, &h.Excluded); err != nil {
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
// old branch stays. atSeq outside [0, head] is ErrInvalidSeq. A seq inside a
// context_exclude turn is rejected because the child would inherit part of a
// turn whose marker lies after the fork point (ADR-134/ADR-136). The child
// inherits origin, cwd and the gateway conversation identity.
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
		if cerr := t.checkForkOpen(sessionID, atSeq); cerr != nil {
			return cerr
		}
		if cerr := t.checkForkExcluded(sessionID, atSeq); cerr != nil {
			return cerr
		}
		if _, cerr := t.CreateSession(Session{
			ID: id, ParentID: sessionID, ForkSeq: atSeq, Origin: src.origin, Cwd: src.cwd,
			Model: src.model, ConfigSHA: src.configSHA, ContextTier: src.contextTier,
		}); cerr != nil {
			return cerr
		}
		return t.inheritGateway(sessionID, id)
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// checkForkOpen rejects a fork point inside a turn that has not ended:
// the child would inherit a live turn whose tool results are still moving
// (SEC13-2). The caller must finish or cancel the turn first.
func (t *Tx) checkForkOpen(sid string, at int64) error {
	var turn string
	err := t.Get(`SELECT s.turn_id FROM events s
		WHERE s.session_id = ?1 AND s.kind = 'turn_start' AND s.seq <= ?2
		  AND NOT EXISTS (SELECT 1 FROM events e WHERE e.session_id = s.session_id
		    AND e.turn_id = s.turn_id AND e.kind = 'turn_end' AND e.seq <= ?2)
		ORDER BY s.seq DESC LIMIT 1`, []any{sid, at}, &turn)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ctxdb: fork open-turn check: %w", err)
	}
	return fmt.Errorf("session %s: open turn %s: %w", sid, turn, ErrSessionBusy)
}

// checkForkExcluded rejects a fork point inside a turn that an
// append-only context_exclude marker excludes after the point: the child
// would otherwise inherit a partial failed turn without its marker.
func (t *Tx) checkForkExcluded(sid string, at int64) error {
	var turn string
	err := t.Get(`SELECT json_extract(x.payload, '$.turn_id')
		FROM events x JOIN events e ON e.session_id = x.session_id
		WHERE x.session_id = ?1 AND x.kind = 'context_exclude' AND x.seq > ?2
		  AND json_extract(x.payload, '$.turn_id') = e.turn_id AND e.seq <= ?2
		LIMIT 1`, []any{sid, at}, &turn)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ctxdb: fork exclude check: %w", err)
	}
	return fmt.Errorf("ctxdb: fork %s at seq %d inside excluded turn %s: %w", sid, at, turn, ErrInvalidSeq)
}

// inheritGateway copies only the parent's gateway_chat scope label to the
// child: it stays in the same principal's scope but a live chat route is
// never moved (SEC13-1; the in-chat /fork owns that). PR #220's CreateSession
// writes the same label from Session.GatewayIdentity, so the write stays
// single once that lands.
func (t *Tx) inheritGateway(parent, child string) error {
	if _, err := t.Exec(`INSERT INTO session_labels(session_id, key, value, created_at)
		SELECT ?2, 'gateway_chat', value, ?3 FROM session_labels
		WHERE session_id = ?1 AND key = 'gateway_chat'
		  AND NOT EXISTS (SELECT 1 FROM session_labels c WHERE c.session_id = ?2 AND c.key = 'gateway_chat')
		ORDER BY id DESC LIMIT 1`, parent, child, t.s.clk.WallMs()); err != nil {
		return fmt.Errorf("ctxdb: inherit gateway scope: %w", err)
	}
	return nil
}

// Head returns the highest own event seq of sessionID (0 for an empty
// session). Ancestor events are not counted: fork_seq is in the session's own
// seq space.
func (s *Store) Head(ctx context.Context, sessionID string) (int64, error) {
	src, err := s.forkSource(ctx, sessionID)
	if err != nil {
		return 0, err
	}
	return src.head, nil
}

// TurnRange returns the first and last own event seq of turnID in sessionID.
// A missing turn is ErrNotFound.
func (s *Store) TurnRange(ctx context.Context, sessionID, turnID string) (first, last int64, err error) {
	err = s.get(ctx, `SELECT coalesce(min(seq),0), coalesce(max(seq),0) FROM events WHERE session_id = ?1 AND turn_id = ?2`,
		[]any{sessionID, turnID}, &first, &last)
	if err != nil {
		return 0, 0, fmt.Errorf("ctxdb: turn %s: %w", turnID, err)
	}
	if last == 0 {
		return 0, 0, fmt.Errorf("turn %s: %w", turnID, ErrNotFound)
	}
	return first, last, nil
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
	if errors.Is(err, ErrNotFound) {
		return f, fmt.Errorf("session %s: %w", sid, ErrNotFound)
	}
	if err != nil {
		return f, fmt.Errorf("ctxdb: session %s: %w", sid, err)
	}
	if labelled {
		return f, fmt.Errorf("ctxdb: session %s: %w", sid, ErrSessionPurging)
	}
	return f, nil
}
