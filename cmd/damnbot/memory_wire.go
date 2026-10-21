package main

import (
	"context"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/memstore"
	"github.com/damnbot/damnbot/internal/memory"
	"github.com/damnbot/damnbot/internal/redact"
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
