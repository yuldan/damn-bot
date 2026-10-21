package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/compaction"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/compactstore"
	"github.com/damnbot/damnbot/internal/ctxdb/memstore"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/secret"
)

func realCodec(t *testing.T, dir string) *secret.Codec {
	t.Helper()
	st, err := secret.New(secret.Options{ConfigDir: filepath.Join(dir, "cfg"), BackendPresent: func() bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	c, err := secret.OpenCodec(context.Background(), dir, st, true)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func openEncrypted(t *testing.T, dir string) *ctxdb.Store {
	t.Helper()
	s, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "cli", Codec: realCodec(t, dir)})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEncryptedWritersSearchAndCanary(t *testing.T) {
	dir := t.TempDir()
	s := openEncrypted(t, dir)
	defer closeStore(t, s)
	ctx := context.Background()
	if _, err := s.CreateSession(ctx, ctxdb.Session{ID: "S", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, "S", nil, ctxdb.Event{Kind: "user_message", Role: "user", Content: "alpha beta 天气预报很准"}); err != nil {
		t.Fatal(err)
	}
	red := redact.New(redact.Options{})
	if _, err := memstore.Put(ctx, s, &memstore.Row{Category: "fact", Scope: "user", Content: "canary-memory", Origin: "user", ScanResult: "{}", CreatedAt: 1},
		&memstore.Decision{Status: "approved", Actor: "user", CreatedAt: 1}, red); err != nil {
		t.Fatal(err)
	}
	store := compactstore.New(s, func() time.Time { return time.Unix(1, 0) })
	if _, err := store.Commit(ctx, "S", &compaction.Commit{
		Attempt: compaction.Attempt{Outcome: compaction.OutcomeSummarized, AtMS: 1000},
		Summary: &compaction.Summary{FromSeq: 1, ToSeq: 1, Content: `{"goal":"canary-summary"}`, Model: "m"},
	}); err != nil {
		t.Fatal(err)
	}
	assertHit(t, s, "alpha beta", "user_message")
	assertHit(t, s, `"alpha beta"`, "user_message")
	assertHit(t, s, "天气", "user_message")
	assertHit(t, s, "canary-summary", "summary_ref")
	mh, err := memstore.Search(ctx, s, &memstore.Query{Text: "canary-memory", Scopes: []string{"user"}, Limit: 5, Now: 2})
	if err != nil || len(mh) == 0 {
		t.Fatalf("memory search = %+v, %v", mh, err)
	}
	assertNoPlaintext(t, dir, "canary-memory", "canary-summary", "alpha beta", "天气预报")
}

type fixedMeter struct{}

func (fixedMeter) Measure(context.Context, string, *compaction.View) (int64, int64, error) {
	return 900, 1000, nil
}

type fixedSummary struct{ content string }

func (s fixedSummary) Summarize(context.Context, *compaction.SummaryRequest) (compaction.Reply, error) {
	return compaction.Reply{Content: s.content, Call: compaction.Call{Provider: "p", Model: "m"}}, nil
}

func TestEncryptedCompactorLoadAndHistory(t *testing.T) {
	dir := t.TempDir()
	s := openEncrypted(t, dir)
	defer closeStore(t, s)
	ctx := context.Background()
	if _, err := s.CreateSession(ctx, ctxdb.Session{ID: "S", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	for i := range 20 {
		content := "message " + string(rune('a'+i)) + " canary-input"
		if _, err := s.Append(ctx, "S", nil, ctxdb.Event{Kind: "user_message", Role: "user", Content: content, Payload: "{}"}); err != nil {
			t.Fatal(err)
		}
	}
	store := compactstore.New(s, func() time.Time { return time.Unix(1, 0) })
	c, err := compaction.NewCompactor(&compaction.Deps{
		Store: store, Summarizer: fixedSummary{content: `{"goal":"canary-compaction","decisions":[],"files":[],"open_items":[],"facts":[]}`},
		Meter: fixedMeter{}, Redactor: redact.New(redact.Options{}), Now: func() time.Time { return time.Unix(1, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.Run(ctx, "S", compaction.ReasonContextLength)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != compaction.OutcomeSummarized && res.Outcome != compaction.OutcomeIneffective {
		t.Fatalf("outcome = %s, cause %s", res.Outcome, res.Cause)
	}
	verifyCompacted(ctx, t, s, store, dir)
}

func verifyCompacted(ctx context.Context, t *testing.T, s *ctxdb.Store, store *compactstore.Store, dir string) {
	t.Helper()
	snap, err := store.Load(ctx, "S")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Summaries) != 1 || !strings.Contains(snap.Summaries[0].Content, "canary-compaction") {
		t.Fatalf("summaries = %+v", snap.Summaries)
	}
	if len(snap.Events) == 0 {
		t.Fatal("no events after boundary")
	}
	for i := range snap.Events {
		e := &snap.Events[i]
		if strings.Contains(e.Content, "S|") || strings.ContainsRune(e.Content, 0) {
			t.Fatalf("event content still identity-prefixed: %q", e.Content)
		}
		if !strings.HasPrefix(e.Content, "message ") || !json.Valid([]byte(e.Payload)) {
			t.Fatalf("event content=%q payload=%q", e.Content, e.Payload)
		}
	}
	evs, err := s.History(ctx, "S", 0)
	if err != nil {
		t.Fatal(err)
	}
	verifySummaryRef(t, evs)
	assertNoPlaintext(t, dir, "canary-input", "canary-compaction")
}

func verifySummaryRef(t *testing.T, evs []ctxdb.Event) {
	t.Helper()
	for i := range evs {
		e := &evs[i]
		if e.Kind != "summary_ref" || e.Content == "" {
			continue
		}
		if !json.Valid([]byte(e.Content)) || e.Content == "{}" || !strings.Contains(e.Content, "canary-compaction") {
			t.Fatalf("summary_ref content = %q", e.Content)
		}
		return
	}
	t.Fatalf("no summary_ref in history: %+v", evs)
}

func TestEncryptedMemoryCap(t *testing.T) {
	dir := t.TempDir()
	s := openEncrypted(t, dir)
	defer closeStore(t, s)
	ctx := context.Background()
	red := redact.New(redact.Options{})
	row := func(content string) (*memstore.Row, *memstore.Decision) {
		return &memstore.Row{Category: "fact", Scope: "user", Content: content, Origin: "user", ScanResult: "{}", CreatedAt: 1},
			&memstore.Decision{Status: "approved", Actor: "user", CreatedAt: 1}
	}
	for range 8 {
		r, d := row(strings.Repeat("a", 500))
		if _, err := memstore.Put(ctx, s, r, d, red); err != nil {
			t.Fatal(err)
		}
	}
	r, d := row(strings.Repeat("b", 500))
	if _, err := memstore.Put(ctx, s, r, d, red); !errors.Is(err, memstore.TooLarge{}) {
		t.Fatalf("ninth row = %v, want TooLarge", err)
	}
}

func TestEncryptedCompactStoreTaintedAndLastCall(t *testing.T) {
	dir := t.TempDir()
	s := openEncrypted(t, dir)
	defer closeStore(t, s)
	ctx := context.Background()
	if _, err := s.CreateSession(ctx, ctxdb.Session{ID: "S", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, "S", nil, ctxdb.Event{Kind: "user_message", Role: "user", Content: "canary-tainted"}); err != nil {
		t.Fatal(err)
	}
	store := compactstore.New(s, func() time.Time { return time.Unix(1, 0) })
	if _, err := store.Commit(ctx, "S", &compaction.Commit{
		Attempt: compaction.Attempt{Outcome: compaction.OutcomeSummarized, Reason: compaction.ReasonContextLength, AtMS: 2000},
		Summary: &compaction.Summary{FromSeq: 1, ToSeq: 1, Content: `{"goal":"canary-tainted"}`, Model: "m", Tainted: true},
	}); err != nil {
		t.Fatal(err)
	}
	snap, err := store.Load(ctx, "S")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Summaries) != 1 || !snap.Summaries[0].Tainted {
		t.Fatalf("summaries = %+v, want tainted", snap.Summaries)
	}
	if snap.LastCall == nil || snap.LastCall.Outcome != compaction.OutcomeSummarized {
		t.Fatalf("last call = %+v", snap.LastCall)
	}
	assertNoPlaintext(t, dir, "canary-tainted")
}

func TestMemoryCapLegacyNullContentLen(t *testing.T) {
	dir := t.TempDir()
	s, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	ctx := context.Background()
	if werr := s.Write(ctx, func(tx *ctxdb.Tx) error {
		var id int64
		if xerr := tx.Get(`INSERT INTO memory(kind, category, scope, name, content, origin, created_at)
			VALUES ('memory','fact','user',NULL,?,'user',1) RETURNING id`, []any{strings.Repeat("a", 500)}, &id); xerr != nil {
			return fmt.Errorf("legacy memory: %w", xerr)
		}
		if _, xerr := tx.Exec(`INSERT INTO memory_decisions(memory_id, status, actor, created_at) VALUES (?, 'approved', 'user', 1)`, id); xerr != nil {
			return fmt.Errorf("legacy decision: %w", xerr)
		}
		return nil
	}); werr != nil {
		t.Fatal(werr)
	}
	red := redact.New(redact.Options{})
	for range 7 {
		if _, perr := memstore.Put(ctx, s, &memstore.Row{Category: "fact", Scope: "user", Content: strings.Repeat("a", 500), Origin: "user", ScanResult: "{}", CreatedAt: 1},
			&memstore.Decision{Status: "approved", Actor: "user", CreatedAt: 1}, red); perr != nil {
			t.Fatal(perr)
		}
	}
	_, err = memstore.Put(ctx, s, &memstore.Row{Category: "fact", Scope: "user", Content: strings.Repeat("b", 500), Origin: "user", ScanResult: "{}", CreatedAt: 1},
		&memstore.Decision{Status: "approved", Actor: "user", CreatedAt: 1}, red)
	if !errors.Is(err, memstore.TooLarge{}) {
		t.Fatalf("legacy NULL content_len cap = %v, want TooLarge", err)
	}
}

func assertHit(t *testing.T, s *ctxdb.Store, q, kind string) {
	t.Helper()
	hits, err := s.Search(context.Background(), ctxdb.Query{Text: q, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Kind == kind {
			return
		}
	}
	t.Fatalf("search %q: no %s hit in %+v", q, kind, hits)
}

func assertNoPlaintext(t *testing.T, dir string, canaries ...string) {
	t.Helper()
	raw, err := os.ReadFile(ctxdb.DBPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range canaries {
		if bytes.Contains(raw, []byte(c)) {
			t.Fatalf("plaintext %q in encrypted DB", c)
		}
	}
}

func closeStore(t *testing.T, s *ctxdb.Store) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Error(err)
	}
}
