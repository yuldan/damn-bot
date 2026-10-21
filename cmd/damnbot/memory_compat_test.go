package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/memstore"
	"github.com/damnbot/damnbot/internal/memory"
	"github.com/damnbot/damnbot/internal/redact"
)

// memAdapter is the composition-root seam between memory.Service and
// memstore (ADR-074, ADR-120): the consumer interface uses memory types, the
// storage uses field-identical memstore types, and this test-owned adapter
// proves the conversion is total. Production wiring lands with the F2 memory
// tools/CLI; keeping it here avoids dead production code before then.
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

func newMemService(t *testing.T) *memory.Service {
	t.Helper()
	store, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := store.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	red := redact.New(redact.Options{})
	svc, err := memory.New(memAdapter{s: store, red: red}, memory.Config{Now: func() int64 { return 1_000 }, Redactor: red})
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// TestMemoryServiceOverMemstore: the quarantine gate holds through real SQL:
// a written entry is invisible to Recall until approved, and approval needs a
// real decision row.
func TestMemoryServiceOverMemstore(t *testing.T) {
	ctx := t.Context()
	svc := newMemService(t)
	rec, err := svc.Propose(ctx, &memory.Proposal{
		Category: memory.CategoryFact, Scope: "user", Content: "decision log lives in ctxdb",
		Origin: memory.OriginImport,
	})
	if err != nil {
		t.Fatal(err)
	}
	hits, err := svc.Recall(ctx, &memory.Recall{Text: "ctxdb", Scopes: []string{"user"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("quarantined entry retrieved: %+v", hits)
	}
	if err = svc.Approve(ctx, rec.ID, "user"); err != nil {
		t.Fatal(err)
	}
	if hits, err = svc.Recall(ctx, &memory.Recall{Text: "ctxdb", Scopes: []string{"user"}}); err != nil || len(hits) != 1 {
		t.Fatalf("approved entry: %d %v", len(hits), err)
	}
}

// TestMemoryRedactionOverMemstore: redaction happens before the FTS index and
// the stored row.
func TestMemoryRedactionOverMemstore(t *testing.T) {
	ctx := t.Context()
	svc := newMemService(t)
	secret := "ghp_" + strings.Repeat("C", 36)
	user, err := svc.Propose(ctx, &memory.Proposal{
		Category: memory.CategoryPreference, Scope: "user", Content: "token " + secret,
		Origin: memory.OriginUser,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(user.Content, secret) || !redact.ContainsSentinel(user.Content) {
		t.Fatalf("proposal leaked the secret: %q", user.Content)
	}
	got, err := svc.Get(ctx, []int64{user.ID})
	if err != nil || len(got) != 1 || strings.Contains(got[0].Content, secret) {
		t.Fatalf("stored secret: %+v %v", got, err)
	}
}
