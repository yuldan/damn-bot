// Package compactstore is the summary storage of internal/compaction (SPEC
// §4.7, §11.2; ADR-074, ADR-122). Like journalstore it imports the consumer
// package for its types (one definition of Summary/Event); the consumer
// never imports storage. Reads honour purge visibility (SPEC §11.6) and
// every write goes through the ctxdb writer, so the lease fence and the
// purge guards of migrations/0008 apply.
package compactstore

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/compaction"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/ledgerstore"
	"github.com/damnbot/damnbot/internal/loop/event"
)

// maxCostUSD bounds a provider-reported cost; larger, negative or NaN values
// are stored as unknown (cost_usd_micros NULL), never as a wrapped integer.
const maxCostUSD = 1e6

// Store implements compaction.Store over a ctxdb.Store.
type Store struct {
	db  *ctxdb.Store
	now func() time.Time
}

var _ compaction.Store = (*Store)(nil)

// New returns a Store; now stamps created_at (nil = time.Now).
func New(db *ctxdb.Store, now func() time.Time) *Store {
	if now == nil {
		now = time.Now
	}
	return &Store{db: db, now: now}
}

const summariesQ = `SELECT json_object('id', s.id, 'from', s.from_seq, 'to', s.to_seq, 'content', s.content,
	'model', s.model, 'in', s.input_tokens, 'out', s.output_tokens,
	'tainted', coalesce(json_extract(e.payload, '$.tainted'), 0))
	FROM summaries s JOIN events e ON e.id = s.event_id WHERE s.session_id = ?1 ORDER BY s.to_seq, s.id`

// eventCols renders one event row as JSON; image attachments are the
// event_attachments whose blob has an image/* mime, in ord order.
const eventCols = `json_object('seq', e.seq, 'kind', e.kind, 'turn', coalesce(e.turn_id, ''),
	'role', coalesce(e.role, ''), 'content', e.content, 'call', coalesce(e.tool_call_id, ''),
	'tool', coalesce(e.tool_name, ''), 'payload', e.payload, 'blob', coalesce(e.blob_sha, ''),
	'images', json((SELECT json_group_array(x.blob_sha) FROM (SELECT a.blob_sha FROM event_attachments a
		JOIN blobs b ON b.sha256 = a.blob_sha WHERE a.event_id = e.id AND b.mime LIKE 'image/%' ORDER BY a.ord) x)))`

// notExcluded drops the events of a turn marked by an append-only
// context_exclude event (GAP-REL-01); the rows stay in the transcript and
// remain recallable, but never reach a summary. Spans (from_seq/to_seq) are
// honoured when present.
const notExcluded = ` AND NOT EXISTS (SELECT 1 FROM events x WHERE x.session_id = e.session_id AND x.kind = 'context_exclude' AND (json_extract(x.payload, '$.turn_id') = e.turn_id OR (coalesce(json_extract(x.payload, '$.from_seq'), 0) > 0 AND e.seq >= json_extract(x.payload, '$.from_seq') AND (coalesce(json_extract(x.payload, '$.to_seq'), 0) = 0 OR e.seq <= json_extract(x.payload, '$.to_seq')))))`

// eventsQ reads the model-visible kinds after the boundary; taskQ the
// newest user message at or before it.
const (
	eventsQ = `SELECT ` + eventCols + ` FROM events e WHERE e.session_id = ?1 AND e.seq > ?2
	AND e.kind IN ('user_message', 'assistant_message', 'tool_result')` + notExcluded + ` ORDER BY e.seq`
	taskQ = `SELECT ` + eventCols + ` FROM events e WHERE e.session_id = ?1 AND e.seq <= ?2
	AND e.kind = 'user_message'` + notExcluded + ` ORDER BY e.seq DESC LIMIT 1`
)

// stateQ reads whether the session is visible (exists, not purging: SPEC
// §11.6) and its Run history: whether any `compaction` event exists and the
// payload of the newest one that called the summarizer.
const stateQ = `SELECT json_object('visible', EXISTS (SELECT 1 FROM sessions s WHERE s.id = ?1 AND NOT EXISTS
		(SELECT 1 FROM session_labels l WHERE l.session_id = s.id AND l.key = 'purge_state')),
	'compacted', EXISTS (SELECT 1 FROM events WHERE session_id = ?1 AND kind = 'compaction'),
	'last', (SELECT json(payload) FROM events WHERE session_id = ?1 AND kind = 'compaction'
		AND json_extract(payload, '$.called') = 1 ORDER BY seq DESC LIMIT 1))`

type summaryRow struct {
	ID      int64  `json:"id"`
	From    int64  `json:"from"`
	To      int64  `json:"to"`
	Content string `json:"content"`
	Model   string `json:"model"`
	In      int64  `json:"in"`
	Out     int64  `json:"out"`
	Tainted int64  `json:"tainted"`
}

type eventRow struct {
	Seq     int64    `json:"seq"`
	Kind    string   `json:"kind"`
	Turn    string   `json:"turn"`
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Call    string   `json:"call"`
	Tool    string   `json:"tool"`
	Payload string   `json:"payload"`
	Blob    string   `json:"blob"`
	Images  []string `json:"images"`
}

// Load returns every summary of a visible session, its Run history, the
// model-visible events after the newest summary and the request it may have
// covered, in one transaction.
func (s *Store) Load(ctx context.Context, sid string) (compaction.Snapshot, error) {
	var snap compaction.Snapshot
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) error {
		err := state(tx, sid, &snap)
		if err != nil {
			return err
		}
		if snap.Summaries, err = summaries(tx, sid); err != nil {
			return err
		}
		if snap.Events, err = events(tx, eventsQ, sid, snap.Boundary()); err != nil || snap.Boundary() == 0 {
			return err
		}
		task, err := events(tx, taskQ, sid, snap.Boundary())
		if len(task) > 0 {
			snap.Task = &task[0]
		}
		return err
	})
	if err != nil {
		return compaction.Snapshot{}, fmt.Errorf("compactstore load: %w", err)
	}
	return snap, nil
}

func summaries(tx *ctxdb.Tx, sid string) ([]compaction.Summary, error) {
	rows, err := tx.Strings(summariesQ, sid)
	if err != nil {
		return nil, fmt.Errorf("summaries: %w", err)
	}
	out := make([]compaction.Summary, len(rows))
	for i, raw := range rows {
		var r summaryRow
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return nil, fmt.Errorf("summary row: %w", err)
		}
		out[i] = compaction.Summary{
			ID: r.ID, FromSeq: r.From, ToSeq: r.To, Content: r.Content, Model: r.Model,
			InputTokens: r.In, OutputTokens: r.Out, Tainted: r.Tainted != 0,
		}
	}
	return out, nil
}

func state(tx *ctxdb.Tx, sid string, snap *compaction.Snapshot) error {
	var raw string
	if err := tx.Get(stateQ, []any{sid}, &raw); err != nil {
		return fmt.Errorf("session state: %w", err)
	}
	var st struct {
		Visible, Compacted int64
		Last               *attemptPayload
	}
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		return fmt.Errorf("compaction state: %w", err)
	}
	if st.Visible == 0 {
		return fmt.Errorf("session %s: %w", sid, ctxdb.ErrNotFound)
	}
	snap.Compacted = st.Compacted != 0
	if l := st.Last; l != nil {
		snap.LastCall = &compaction.Attempt{Outcome: l.Outcome, Reason: l.Reason, AtMS: l.AtMS}
	}
	return nil
}

func events(tx *ctxdb.Tx, q, sid string, boundary int64) ([]compaction.Event, error) {
	rows, err := tx.Strings(q, sid, boundary)
	if err != nil {
		return nil, fmt.Errorf("events: %w", err)
	}
	out := make([]compaction.Event, len(rows))
	for i, raw := range rows {
		var r eventRow
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return nil, fmt.Errorf("event row: %w", err)
		}
		out[i] = compaction.Event{
			Seq: r.Seq, Kind: event.Kind(r.Kind), TurnID: r.Turn, Role: r.Role, Content: r.Content,
			ToolCallID: r.Call, ToolName: r.Tool, Payload: r.Payload, BlobSHA: r.Blob, Images: r.Images,
		}
	}
	return out, nil
}

// llmPayload mirrors the loop's llm_call payload plus the purpose.
type llmPayload struct {
	Purpose     string   `json:"purpose"`
	Model       string   `json:"model"`
	Attempts    int      `json:"attempts"`
	Input       int64    `json:"input_tokens"`
	CachedInput int64    `json:"cached_input_tokens"`
	Output      int64    `json:"output_tokens"`
	CostUSD     *float64 `json:"cost_usd,omitempty"`
}

type refPayload struct {
	FromSeq int64 `json:"from_seq"`
	ToSeq   int64 `json:"to_seq"`
	Tainted bool  `json:"tainted,omitempty"`
}

// attemptPayload is the `compaction` event payload of one Run.
type attemptPayload struct {
	Outcome compaction.Outcome `json:"outcome"`
	Reason  compaction.Reason  `json:"reason"`
	AtMS    int64              `json:"at_ms"`
	Called  bool               `json:"called"`
}

// Commit records the call, the summary and the Attempt in one transaction;
// a failure leaves no row behind.
func (s *Store) Commit(ctx context.Context, sid string, c *compaction.Commit) (int64, error) {
	var id int64
	err := s.db.Write(ctx, func(tx *ctxdb.Tx) (err error) {
		id, err = commit(tx, sid, c, s.now().UnixMilli())
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("compactstore commit: %w", err)
	}
	return id, nil
}

func commit(tx *ctxdb.Tx, sid string, c *compaction.Commit, now int64) (int64, error) {
	if c.Call != nil {
		if err := call(tx, sid, c.TurnID, c.Call, now); err != nil {
			return 0, err
		}
	}
	var id int64
	if c.Summary != nil {
		var err error
		if id, err = summary(tx, sid, c.TurnID, c.Summary, now); err != nil {
			return 0, err
		}
	}
	a := &c.Attempt
	payload, err := json.Marshal(attemptPayload{Outcome: a.Outcome, Reason: a.Reason, AtMS: a.AtMS, Called: a.Outcome.Called()})
	if err != nil {
		return 0, fmt.Errorf("compaction payload: %w", err)
	}
	if _, err := tx.AppendEvent(sid, &ctxdb.Event{Kind: string(event.Compaction), TurnID: c.TurnID, Payload: string(payload)}); err != nil {
		return 0, fmt.Errorf("compaction event: %w", err)
	}
	return id, nil
}

// call appends the llm_call event and its purpose=compaction ledger row.
func call(tx *ctxdb.Tx, sid, turn string, c *compaction.Call, now int64) error {
	u := &c.Usage
	payload, err := json.Marshal(llmPayload{
		Purpose: "compaction", Model: c.Model, Attempts: c.Attempts,
		Input: u.Input, CachedInput: u.CachedInput, Output: u.Output, CostUSD: u.CostUSD,
	})
	if err != nil {
		return fmt.Errorf("llm_call payload: %w", err)
	}
	callID, err := tx.AppendEvent(sid, &ctxdb.Event{Kind: string(event.LLMCall), TurnID: turn, Payload: string(payload)})
	if err != nil {
		return fmt.Errorf("llm_call: %w", err)
	}
	micros, source := cost(u.CostUSD)
	if _, err = ledgerstore.Insert(tx, &ledgerstore.Row{
		SessionID: sid, TurnID: turn, EventID: callID, Purpose: "compaction",
		Provider: c.Provider, Model: c.Model, InputTokens: u.Input, CachedInputTokens: u.CachedInput,
		OutputTokens: u.Output, CostMicros: micros, CostSource: source, PricingVersion: "none",
		Attempts: int64(max(c.Attempts, 1)), LatencyMS: c.LatencyMS, TTFTMS: -1, CreatedAt: now,
	}); err != nil {
		return fmt.Errorf("ledger: %w", err)
	}
	return nil
}

// summary appends the summary_ref event and the summaries row.
func summary(tx *ctxdb.Tx, sid, turn string, sum *compaction.Summary, now int64) (int64, error) {
	ref, err := json.Marshal(refPayload{FromSeq: sum.FromSeq, ToSeq: sum.ToSeq, Tainted: sum.Tainted})
	if err != nil {
		return 0, fmt.Errorf("summary_ref payload: %w", err)
	}
	refID, err := tx.AppendEvent(sid, &ctxdb.Event{Kind: string(event.SummaryRef), TurnID: turn, Payload: string(ref)})
	if err != nil {
		return 0, fmt.Errorf("summary_ref: %w", err)
	}
	var id int64
	if err = tx.Get(`INSERT INTO summaries(session_id, from_seq, to_seq, content, model, input_tokens, output_tokens,
		event_id, created_at) VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9) RETURNING id`,
		[]any{sid, sum.FromSeq, sum.ToSeq, sum.Content, sum.Model, sum.InputTokens, sum.OutputTokens, refID, now}, &id); err != nil {
		return 0, fmt.Errorf("summaries: %w", err)
	}
	return id, nil
}

// cost converts a provider cost to micro-USD; out of range is unknown.
func cost(usd *float64) (*int64, string) {
	if usd == nil || !(*usd >= 0 && *usd <= maxCostUSD) {
		return nil, "unknown"
	}
	m := int64(*usd*1e6 + 0.5)
	return &m, "provider"
}
