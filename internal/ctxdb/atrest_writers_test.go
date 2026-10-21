package ctxdb_test

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/compaction"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/compactstore"
	"github.com/damnbot/damnbot/internal/ctxdb/memstore"
	"github.com/damnbot/damnbot/internal/redact"
)

type writerCodec struct{}

func (writerCodec) ID() string { return "writer" }
func (writerCodec) Seal(scope string, p []byte) ([]byte, error) {
	out := append([]byte(scope+"|"), p...)
	for i := len(scope) + 1; i < len(out); i++ {
		out[i] ^= 0xa5
	}
	return out, nil
}

func (writerCodec) Open(scope string, b []byte) ([]byte, error) {
	in := make([]byte, len(b))
	copy(in, b)
	for i := len(scope) + 1; i < len(in); i++ {
		in[i] ^= 0xa5
	}
	return in[len(scope)+1:], nil
}
func (writerCodec) Hash(s string) string     { return "h:" + s }
func (writerCodec) FTS(tokens string) string { return strings.ToUpper(tokens) }

func TestAtRestWritersSearchAndCanary(t *testing.T) {
	dir := t.TempDir()
	s, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "cli", Codec: writerCodec{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := s.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	ctx := context.Background()
	if _, cerr := s.CreateSession(ctx, ctxdb.Session{ID: "S", Cwd: "/w"}); cerr != nil {
		t.Fatal(cerr)
	}
	if _, aerr := s.Append(ctx, "S", nil, ctxdb.Event{Kind: "user_message", Role: "user", Content: "alpha beta 天气预报很准"}); aerr != nil {
		t.Fatal(aerr)
	}
	red := redact.New(redact.Options{})
	if _, perr := memstore.Put(ctx, s, &memstore.Row{Category: "fact", Scope: "user", Content: "canary-memory", Origin: "user", ScanResult: "{}", CreatedAt: 1},
		&memstore.Decision{Status: "approved", Actor: "user", CreatedAt: 1}, red); perr != nil {
		t.Fatal(perr)
	}
	store := compactstore.New(s, func() time.Time { return time.Unix(1, 0) })
	if _, cerr := store.Commit(ctx, "S", &compaction.Commit{
		Attempt: compaction.Attempt{Outcome: compaction.OutcomeSummarized, AtMS: 1000},
		Summary: &compaction.Summary{FromSeq: 1, ToSeq: 1, Content: `{"goal":"canary-summary"}`, Model: "m"},
	}); cerr != nil {
		t.Fatal(cerr)
	}
	assertHit(t, s, "alpha beta", "user_message")
	assertHit(t, s, "天气", "user_message")
	assertHit(t, s, "canary-summary", "summary_ref")
	mh, err := memstore.Search(ctx, s, &memstore.Query{Text: "canary-memory", Scopes: []string{"user"}, Limit: 5, Now: 2})
	if err != nil || len(mh) == 0 {
		t.Fatalf("memory search = %+v, %v", mh, err)
	}
	raw, err := os.ReadFile(ctxdb.DBPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"canary-memory", "canary-summary", "alpha beta", "天气预报"} {
		if bytes.Contains(raw, []byte(c)) {
			t.Fatalf("plaintext %q in encrypted DB", c)
		}
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

type fixedMeter struct{}

func (fixedMeter) Measure(context.Context, string, *compaction.View) (int64, int64, error) {
	return 900, 1000, nil
}

type fixedSummary struct{ content string }

func (s fixedSummary) Summarize(context.Context, *compaction.SummaryRequest) (compaction.Reply, error) {
	return compaction.Reply{Content: s.content, Call: compaction.Call{Provider: "p", Model: "m"}}, nil
}

func TestAtRestCompactorEncrypted(t *testing.T) {
	dir := t.TempDir()
	s, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "cli", Codec: writerCodec{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := s.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	ctx := context.Background()
	if _, cerr := s.CreateSession(ctx, ctxdb.Session{ID: "S", Cwd: "/w"}); cerr != nil {
		t.Fatal(cerr)
	}
	for i := range 20 {
		if _, aerr := s.Append(ctx, "S", nil, ctxdb.Event{Kind: "user_message", Role: "user", Content: "message " + string(rune('a'+i)) + " canary-input"}); aerr != nil {
			t.Fatal(aerr)
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
	snap, err := store.Load(ctx, "S")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Summaries) != 1 || !strings.Contains(snap.Summaries[0].Content, "canary-compaction") {
		t.Fatalf("summaries = %+v", snap.Summaries)
	}
	assertNoPlaintext(t, dir, "canary-input", "canary-compaction")
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
