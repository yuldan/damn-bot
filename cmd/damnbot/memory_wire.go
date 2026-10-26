package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/memstore"
	"github.com/damnbot/damnbot/internal/memory"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/tools"
)

// openMemory returns the memory service over db (memstore), redacting with
// red before every write (ADR-120 butir 5).
func openMemory(db *ctxdb.Store, red *redact.Redactor) (*memory.Service, error) {
	svc, err := memory.New(memAdapter{s: db, red: red}, memory.Config{Now: func() int64 { return time.Now().UnixMilli() }, Redactor: red})
	if err != nil {
		return nil, fmt.Errorf("memory service: %w", err)
	}
	return svc, nil
}

// withMemoryTools opens the memory service once per run and appends the two
// registry tools that share it (TOOL-070, TOOL-073; ADR-188).
func withMemoryTools(list []tools.Tool, db *ctxdb.Store, red *redact.Redactor) ([]tools.Tool, error) {
	svc, err := openMemory(db, red)
	if err != nil {
		return nil, err
	}
	return append(list, tools.NewMemory(&memoryToolBackend{svc: svc, db: db}),
		tools.NewSessionSearch(ctxSearch{db: db})), nil
}

// memAdapter is the composition-root seam between memory.Service and
// memstore (ADR-074, ADR-120): the consumer interface uses memory types, the
// storage uses field-identical memstore types, and the conversion is total.
type memAdapter struct {
	s   *ctxdb.Store
	red *redact.Redactor
}

func (a memAdapter) Insert(ctx context.Context, r *memory.Record, d *memory.Decision) (int64, error) {
	id, err := memstore.Put(ctx, a.s, memRow(r), memDecision(d), a.red)
	if err != nil {
		return 0, fmt.Errorf("mem adapter insert: %w", err)
	}
	return id, nil
}

func (a memAdapter) Decide(ctx context.Context, id int64, scope string, d *memory.Decision) error {
	if err := memstore.Decide(ctx, a.s, id, scope, memDecision(d)); err != nil {
		return fmt.Errorf("mem adapter decide: %w", err)
	}
	return nil
}

func (a memAdapter) Pin(ctx context.Context, id int64, scope string, pinned bool, actor memory.Actor, at int64) error {
	if err := memstore.Pin(ctx, a.s, id, scope, pinned, string(actor), at); err != nil {
		return fmt.Errorf("mem adapter pin: %w", err)
	}
	return nil
}

func (a memAdapter) Search(ctx context.Context, q *memory.Query) ([]memory.Hit, error) {
	hits, err := memstore.Search(ctx, a.s, &memstore.Query{
		Text: q.Text, Scopes: q.Scopes, Statuses: statuses(q.Statuses), Limit: q.Limit, Now: q.Now,
	})
	if err != nil {
		return nil, fmt.Errorf("mem adapter search: %w", err)
	}
	out := make([]memory.Hit, len(hits))
	for i := range hits {
		out[i] = memory.Hit{Record: memRecord(&hits[i].Row)}
	}
	return out, nil
}

func (a memAdapter) Get(ctx context.Context, ids []int64) ([]memory.Record, error) {
	rows, err := memstore.Get(ctx, a.s, ids)
	if err != nil {
		return nil, fmt.Errorf("mem adapter get: %w", err)
	}
	out := make([]memory.Record, len(rows))
	for i := range rows {
		out[i] = memRecord(&rows[i])
	}
	return out, nil
}

func memRow(r *memory.Record) *memstore.Row {
	return &memstore.Row{
		ID: r.ID, Category: string(r.Category), Scope: r.Scope, Content: r.Content,
		Origin: string(r.Origin), SourceSession: r.SourceSession, EvidenceEventID: r.EvidenceEventID,
		SupersedesID: r.SupersedesID, ExpiresAt: r.ExpiresAt, ScanResult: r.ScanResult,
		CreatedAt: r.CreatedAt, Status: string(r.Status), Pinned: r.Pinned, LastConfirmedAt: r.LastConfirmedAt,
	}
}

func memRecord(r *memstore.Row) memory.Record {
	return memory.Record{
		ID: r.ID, Category: memory.Category(r.Category), Scope: r.Scope, Content: r.Content,
		Origin: memory.Origin(r.Origin), SourceSession: r.SourceSession, EvidenceEventID: r.EvidenceEventID,
		SupersedesID: r.SupersedesID, ExpiresAt: r.ExpiresAt, ScanResult: r.ScanResult,
		CreatedAt: r.CreatedAt, Status: memory.Status(r.Status), Pinned: r.Pinned,
		LastConfirmedAt: r.LastConfirmedAt,
	}
}

func memDecision(d *memory.Decision) *memstore.Decision {
	return &memstore.Decision{
		Status: string(d.Status), Actor: string(d.Actor), Reason: d.Reason, CreatedAt: d.CreatedAt,
	}
}

func statuses(in []memory.Status) []string {
	out := make([]string, len(in))
	for i := range in {
		out[i] = string(in[i])
	}
	return out
}

type memoryToolBackend struct {
	svc *memory.Service
	db  *ctxdb.Store
}

// evidence returns the tool_dispatch event (and its session) for a call id.
func (b *memoryToolBackend) evidence(ctx context.Context, callID string) (eventID int64, sessionID string, err error) {
	if callID == "" {
		return 0, "", tools.ErrMemoryInvalid
	}
	err = b.db.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT id, session_id FROM events
			WHERE kind = 'tool_dispatch' AND tool_call_id = ?1 ORDER BY id DESC LIMIT 1`,
			[]any{callID}, &eventID, &sessionID)
	})
	if err != nil {
		if errors.Is(err, ctxdb.ErrNotFound) {
			return 0, "", fmt.Errorf("%w: no dispatch event for call %q", tools.ErrMemoryInvalid, callID)
		}
		return 0, "", fmt.Errorf("memory evidence: %w", err)
	}
	return eventID, sessionID, nil
}

func (b *memoryToolBackend) proposal(ctx context.Context, req tools.MemoryAdd) (*memory.Proposal, error) {
	eventID, sid, err := b.evidence(ctx, req.CallID)
	if err != nil {
		return nil, err
	}
	return &memory.Proposal{
		Category: memory.Category(req.Category), Scope: req.Scope, Content: req.Content,
		Origin: memory.OriginAgent, SourceSession: sid, EvidenceEventID: &eventID,
	}, nil
}

func (b *memoryToolBackend) Add(ctx context.Context, req tools.MemoryAdd) (int64, string, error) {
	p, err := b.proposal(ctx, req)
	if err != nil {
		return 0, "", err
	}
	rec, err := b.svc.Propose(ctx, p)
	if err != nil {
		return 0, "", memToolErr(err)
	}
	return rec.ID, string(rec.Status), nil
}

// Replace supersedes an agent-written note: the target must be an agent-origin
// entry (SEC17-3) and the new entry is stored before the old one is retired, so
// a rejected or failed proposal leaves the old row intact (SEC17-4).
func (b *memoryToolBackend) Replace(ctx context.Context, req tools.MemoryAdd, id int64) (int64, string, error) {
	recs, err := b.svc.Get(ctx, []int64{id})
	if err != nil {
		return 0, "", memToolErr(err)
	}
	if len(recs) != 1 {
		return 0, "", fmt.Errorf("%w: memory %d", tools.ErrMemoryNotFound, id)
	}
	if recs[0].Origin != memory.OriginAgent {
		return 0, "", fmt.Errorf("%w: memory %d is origin %q", tools.ErrMemoryDenied, id, recs[0].Origin)
	}
	p, err := b.proposal(ctx, req)
	if err != nil {
		return 0, "", err
	}
	rec, err := b.svc.Propose(ctx, p)
	if err != nil {
		return 0, "", memToolErr(err)
	}
	if rec.Status == memory.StatusRejected {
		return rec.ID, string(rec.Status), nil
	}
	if rerr := b.svc.RetireByAgent(ctx, id, req.Scope); rerr != nil {
		return 0, "", memToolErr(rerr)
	}
	return rec.ID, string(rec.Status), nil
}

// Remove retires an agent-written note; user memory fails closed (SEC17-3).
func (b *memoryToolBackend) Remove(ctx context.Context, id int64, scope string) error {
	if err := b.svc.RetireByAgent(ctx, id, scope); err != nil {
		return memToolErr(err)
	}
	return nil
}

func (b *memoryToolBackend) Read(ctx context.Context, query, scope string, k int) ([]tools.MemoryEntry, error) {
	hits, err := b.svc.Recall(ctx, &memory.Recall{Text: query, Scopes: []string{scope}, Limit: k})
	if err != nil {
		return nil, memToolErr(err)
	}
	out := make([]tools.MemoryEntry, len(hits))
	for i := range hits {
		r := &hits[i].Record
		out[i] = tools.MemoryEntry{ID: r.ID, Category: string(r.Category), Scope: r.Scope, Content: r.Content, Pinned: r.Pinned}
	}
	return out, nil
}

// memToolErr maps memory/memstore errors onto the tools sentinels.
func memToolErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, memstore.ErrNotFound), errors.Is(err, ctxdb.ErrNotFound):
		return fmt.Errorf("%w: %w", tools.ErrMemoryNotFound, err)
	case errors.Is(err, memstore.ErrNotAgent):
		return fmt.Errorf("%w: %w", tools.ErrMemoryDenied, err)
	case errors.Is(err, memory.ErrTooLarge):
		return fmt.Errorf("%w: %w", tools.ErrMemoryFull, err)
	case errors.Is(err, memory.ErrInvalid), errors.Is(err, memory.ErrScope),
		errors.Is(err, memstore.ErrInvalid), errors.Is(err, memstore.ErrScope),
		errors.Is(err, memstore.ErrTransition):
		return fmt.Errorf("%w: %w", tools.ErrMemoryInvalid, err)
	}
	return err
}

// ctxSearch adapts ctxdb.Search to the session_search tool: project-scoped
// (SEC17-1) and without context_exclude hits (SEC17-2).
type ctxSearch struct{ db *ctxdb.Store }

func (s ctxSearch) Search(ctx context.Context, query string, k int, cwd string) ([]tools.SessionHit, error) {
	if cwd == "" { // fail closed: an unknown project is not "all projects"
		return nil, errors.New("session search: no workspace scope")
	}
	hits, err := s.db.Search(ctx, ctxdb.Query{Text: query, Limit: k, Cwd: cwd})
	if err != nil {
		return nil, fmt.Errorf("session search: %w", err)
	}
	out := make([]tools.SessionHit, 0, len(hits))
	for i := range hits {
		if hits[i].Excluded {
			continue
		}
		out = append(out, tools.SessionHit{
			SessionID: hits[i].SessionID, EventID: hits[i].EventID,
			Kind: hits[i].Kind, Snippet: hits[i].Snippet, Score: hits[i].Score,
		})
	}
	return out, nil
}
