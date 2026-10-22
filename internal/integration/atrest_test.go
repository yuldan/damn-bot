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

	"github.com/damnbot/damnbot/internal/backup"
	"github.com/damnbot/damnbot/internal/compaction"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/carrierstore"
	"github.com/damnbot/damnbot/internal/ctxdb/compactstore"
	"github.com/damnbot/damnbot/internal/ctxdb/memstore"
	"github.com/damnbot/damnbot/internal/ctxdb/skillstore"
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
	ids, err := s.Append(ctx, "S", nil, ctxdb.Event{Kind: "user_message", Role: "user", Content: "alpha beta 天气预报很准"})
	if err != nil {
		t.Fatal(err)
	}
	eid := ids[0]
	assertEncryptedCarrier(t, s, eid, []byte("canary-carrier"))
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
	assertHit(t, s, `"alpha beta"`, "user_message")
	assertHit(t, s, "天气", "user_message")
	assertHit(t, s, "canary-summary", "summary_ref")
	mh, err := memstore.Search(ctx, s, &memstore.Query{Text: "canary-memory", Scopes: []string{"user"}, Limit: 5, Now: 2})
	if err != nil || len(mh) == 0 {
		t.Fatalf("memory search = %+v, %v", mh, err)
	}
	assertNoPlaintext(t, dir, "canary-memory", "canary-summary", "canary-carrier", "alpha beta", "天气预报")
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

func TestCarrierRestoreConversionLoad(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	st, err := secret.New(secret.Options{ConfigDir: filepath.Join(root, "keys"), BackendPresent: func() bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	bkey, err := st.Backup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	eid, payload := seedCarrierSource(t, src)
	archive := filepath.Join(root, "a.dmbk")
	if _, cerr := backup.Create(context.Background(), &backup.CreateOptions{DataDir: src, ConfigDir: filepath.Join(root, "cfg"), Out: archive, Key: bkey}); cerr != nil {
		t.Fatal(cerr)
	}
	dst := filepath.Join(root, "dst")
	if _, rerr := backup.Restore(context.Background(), &backup.RestoreOptions{DataDir: dst, ConfigDir: filepath.Join(root, "dstcfg"), Archive: archive, Key: bkey, KeyStore: st, Encrypt: true}); rerr != nil {
		t.Fatal(rerr)
	}
	verifyCarrierRestore(t, dst, st, eid, payload)
}

func seedCarrierSource(t *testing.T, src string) (int64, []byte) {
	t.Helper()
	plain, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: src, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if _, cerr := plain.CreateSession(context.Background(), ctxdb.Session{ID: "S", Cwd: "/w"}); cerr != nil {
		t.Fatal(cerr)
	}
	ids, aerr := plain.Append(context.Background(), "S", nil, ctxdb.Event{Kind: "assistant_message", Role: "assistant", Content: "reply", Payload: "{}"})
	if aerr != nil {
		t.Fatal(aerr)
	}
	eid := ids[0]
	payload := []byte("canary-carrier-restore")
	if _, serr := (carrierstore.Store{DB: plain}).Save(context.Background(), "S", eid, []carrierstore.Carrier{{Ord: 0, Family: "openai", Model: "m", Kind: carrierstore.KindThoughtSignature, Anchor: "a", Payload: payload, Replayable: true, CreatedAt: 1}}); serr != nil {
		t.Fatal(serr)
	}
	if cerr := plain.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	return eid, payload
}

func verifyCarrierRestore(t *testing.T, dst string, st *secret.Store, eid int64, payload []byte) {
	t.Helper()
	codec, err := secret.OpenCodec(context.Background(), dst, st, false)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dst, Kind: "cli", Codec: codec})
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, enc)
	got, err := (carrierstore.Store{DB: enc}).Load(context.Background(), "S", []int64{eid})
	if err != nil || len(got) != 1 {
		t.Fatalf("load = %+v, %v", got, err)
	}
	keyed := enc.KeyedHash("carrier\x00" + string(payload))
	if !bytes.Equal(got[0].Payload, payload) || got[0].SHA != keyed {
		t.Fatalf("carrier = %+v, want payload %q sha %s", got[0], payload, keyed)
	}
	// Projection input: the loaded bytes, family/model/kind and plaintext SHA
	// are exactly what the provider carrier projection consumes.
	proj := struct {
		Family, Model, Kind, SHA string
		Raw                      []byte
	}{got[0].Family, got[0].Model, string(got[0].Kind), got[0].SHA, got[0].Payload}
	if proj.Family != "openai" || proj.Model != "m" || proj.Kind != string(carrierstore.KindThoughtSignature) || !bytes.Equal(proj.Raw, payload) {
		t.Fatalf("projection input = %+v", proj)
	}
	assertNoPlaintext(t, dst, "canary-carrier-restore")
}

func assertEncryptedCarrier(t *testing.T, s *ctxdb.Store, eid int64, payload []byte) {
	t.Helper()
	ctx := context.Background()
	cs := carrierstore.Store{DB: s}
	refs, err := cs.Save(ctx, "S", eid, []carrierstore.Carrier{{Ord: 0, Family: "openai", Model: "m", Kind: carrierstore.KindThoughtSignature, Anchor: "a", Payload: payload, Replayable: true, CreatedAt: 1}})
	if err != nil {
		t.Fatal(err)
	}
	keyed := s.KeyedHash("carrier\x00" + string(payload))
	if refs[0].SHA != keyed || cs.PayloadSHA(payload) != keyed {
		t.Fatalf("refs sha = %s, store sha = %s, want %s", refs[0].SHA, cs.PayloadSHA(payload), keyed)
	}
	ids, err := cs.BySHA(ctx, "S", []string{keyed})
	if err != nil || len(ids) != 1 || ids[0] != eid {
		t.Fatalf("by sha = %+v, %v", ids, err)
	}
	got, err := cs.Load(ctx, "S", []int64{eid})
	if err != nil || len(got) != 1 || !bytes.Equal(got[0].Payload, payload) || got[0].SHA != keyed {
		t.Fatalf("carrier load = %+v, %v", got, err)
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

func TestEncryptedHistoryExcludedTurns(t *testing.T) {
	dir := t.TempDir()
	s := openEncrypted(t, dir)
	ctx := context.Background()
	if _, err := s.CreateSession(ctx, ctxdb.Session{ID: "S", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	appendEv := func(ev ctxdb.Event) {
		t.Helper()
		if _, err := s.Append(ctx, "S", nil, ev); err != nil {
			t.Fatal(err)
		}
	}
	appendEv(ctxdb.Event{Kind: "user_message", Role: "user", TurnID: "T1", Content: "canary-excluded-question", Payload: "{}"})
	appendEv(ctxdb.Event{Kind: "assistant_message", Role: "assistant", TurnID: "T1", Content: "canary-excluded-answer", Payload: "{}"})
	appendEv(ctxdb.Event{Kind: "context_exclude", TurnID: "T1", Payload: `{"turn_id":"T1","reason":"provider_error"}`})
	appendEv(ctxdb.Event{Kind: "user_message", Role: "user", TurnID: "T2", Content: "canary-visible", Payload: "{}"})
	evs, err := s.History(ctx, "S", 0)
	if err != nil {
		t.Fatal(err)
	}
	seenGood, seenExcluded := false, false
	for _, e := range evs {
		if strings.Contains(e.Content, "canary-excluded") {
			seenExcluded = true
		}
		if e.Content == "canary-visible" {
			seenGood = true
		}
		if strings.Contains(e.Content, "S|") || strings.ContainsRune(e.Content, 0) {
			t.Fatalf("identity prefix leaked: %q", e.Content)
		}
	}
	if !seenGood || seenExcluded {
		t.Fatalf("history = %+v, want visible and no excluded turn", evs)
	}
	closeStore(t, s)
}

func TestEncryptedReadOnlyOpen(t *testing.T) {
	dir := t.TempDir()
	s := openEncrypted(t, dir)
	ctx := context.Background()
	if _, err := s.CreateSession(ctx, ctxdb.Session{ID: "S", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, "S", nil, ctxdb.Event{Kind: "user_message", Role: "user", Content: "canary-readonly", Payload: "{}"}); err != nil {
		t.Fatal(err)
	}
	closeStore(t, s)
	codec := realCodec(t, dir)
	ro, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dir, Kind: "cli", ReadOnly: true, Codec: codec})
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, ro)
	evs, err := ro.History(ctx, "S", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Content != "canary-readonly" {
		t.Fatalf("read-only history = %+v", evs)
	}
	assertNoPlaintext(t, dir, "canary-readonly")
}

func TestEncryptedSearchAndSummaryExclusions(t *testing.T) {
	dir := t.TempDir()
	s := openEncrypted(t, dir)
	defer closeStore(t, s)
	seedExcludedTurn(t, s)
	assertExclusion(t, s)
}

func TestPlaintextSearchAndSummaryExclusions(t *testing.T) {
	s, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: t.TempDir(), Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore(t, s)
	seedExcludedTurn(t, s)
	assertExclusion(t, s)
}

func seedExcludedTurn(t *testing.T, s *ctxdb.Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.CreateSession(ctx, ctxdb.Session{ID: "S", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	evs := []ctxdb.Event{
		{Kind: "user_message", TurnID: "T1", Role: "user", Content: "canary-poison", Payload: "{}"},
		{Kind: "assistant_message", TurnID: "T1", Role: "assistant", Content: "poison answer", Payload: "{}"},
		{Kind: "context_exclude", TurnID: "T1", Payload: `{"turn_id":"T1","reason":"provider_error"}`},
		{Kind: "user_message", TurnID: "T2", Role: "user", Content: "canary-good", Payload: "{}"},
	}
	for i := range evs {
		if _, err := s.Append(ctx, "S", nil, evs[i]); err != nil {
			t.Fatal(err)
		}
	}
}

func assertExclusion(t *testing.T, s *ctxdb.Store) {
	t.Helper()
	hits, err := s.Search(context.Background(), ctxdb.Query{Text: "poison", Limit: 5})
	if err != nil || len(hits) == 0 {
		t.Fatalf("poison hits = %+v, %v", hits, err)
	}
	for i := range hits {
		if !hits[i].Excluded {
			t.Fatalf("poison hit not marked excluded: %+v", hits[i])
		}
	}
	store := compactstore.New(s, func() time.Time { return time.Unix(1, 0) })
	if _, cerr := store.Commit(context.Background(), "S", &compaction.Commit{
		Attempt: compaction.Attempt{Outcome: compaction.OutcomeSummarized, AtMS: 1000},
		Summary: &compaction.Summary{FromSeq: 1, ToSeq: 3, Content: `{"goal":"canary-poison"}`, Model: "m"},
	}); cerr != nil {
		t.Fatal(cerr)
	}
	snap, err := store.Load(context.Background(), "S")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Summaries) != 0 {
		t.Fatalf("summary covering excluded turn survived: %+v", snap.Summaries)
	}
}

func TestFTSCodecLockerQuery(t *testing.T) {
	for _, encrypted := range []bool{false, true} {
		dir := t.TempDir()
		var s *ctxdb.Store
		var err error
		if encrypted {
			s = openEncrypted(t, dir)
		} else {
			s, err = ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "cli"})
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, perr := memstore.Put(context.Background(), s, &memstore.Row{Category: "fact", Scope: "user", Content: "my locker number is 4417", Origin: "user", ScanResult: "{}", CreatedAt: 1},
			&memstore.Decision{Status: "approved", Actor: "user", CreatedAt: 1}, redact.New(redact.Options{})); perr != nil {
			t.Fatal(perr)
		}
		hits, err := memstore.Search(context.Background(), s, &memstore.Query{Text: "What is my locker number?", Scopes: []string{"user"}, Limit: 3, Now: 2})
		if err != nil || len(hits) == 0 || !strings.Contains(hits[0].Row.Content, "4417") {
			t.Fatalf("encrypted=%v locker hits = %+v, %v", encrypted, hits, err)
		}
		closeStore(t, s)
	}
}

func TestEncryptedIndexHidesOperatorWords(t *testing.T) {
	dir := t.TempDir()
	s := openEncrypted(t, dir)
	defer closeStore(t, s)
	ctx := context.Background()
	if _, err := s.CreateSession(ctx, ctxdb.Session{ID: "S", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(ctx, "S", nil, ctxdb.Event{Kind: "user_message", Role: "user", Content: "OR AND NOT canary-operator", Payload: "{}"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Write(ctx, func(tx *ctxdb.Tx) error {
		for _, word := range []string{"OR", "AND", "NOT"} {
			ids, err := tx.Ints(`SELECT rowid FROM events_fts WHERE events_fts MATCH ?`, `"`+word+`"`)
			if err != nil {
				return fmt.Errorf("fts %s: %w", word, err)
			}
			if len(ids) != 0 {
				t.Fatalf("plaintext operator token %q in encrypted FTS index", word)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReadOnlyCodecErrors(t *testing.T) {
	ctx := context.Background()
	empty := t.TempDir()
	s, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: empty, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	closeStore(t, s)
	codec := realCodec(t, empty)
	if _, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: empty, Kind: "cli", ReadOnly: true, Codec: codec}); !errors.Is(err, ctxdb.ErrReadOnly) {
		t.Fatalf("empty read-only encrypted open = %v, want ErrReadOnly", err)
	}

	enc := t.TempDir()
	es := openEncrypted(t, enc)
	if _, err := es.CreateSession(ctx, ctxdb.Session{ID: "S", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	closeStore(t, es)
	if _, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: enc, Kind: "cli", ReadOnly: true}); !errors.Is(err, ctxdb.ErrEncrypted) {
		t.Fatalf("read-only encrypted open without codec = %v, want ErrEncrypted", err)
	}
	wrong := realCodec(t, t.TempDir())
	if _, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: enc, Kind: "cli", ReadOnly: true, Codec: wrong}); !errors.Is(err, ctxdb.ErrEncrypted) {
		t.Fatalf("read-only encrypted open with wrong DEK = %v, want ErrEncrypted", err)
	}
}

func TestEncryptedSkillSearchOperatorsAndCJK(t *testing.T) {
	dir := t.TempDir()
	s := openEncrypted(t, dir)
	defer closeStore(t, s)
	r := &skillstore.Row{Name: "日本語スキル", Path: "/s", SHA256: strings.Repeat("a", 64), Description: "deploy checklist", Body: "deploy checklist with OR NEAR NOT words", Source: "user"}
	r.Name = "日本語スキル"
	if _, err := skillstore.Index(context.Background(), s, r, 100); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"deploy AND", "deploy OR", "deploy NOT", "deploy NEAR"} {
		if _, err := skillstore.Search(context.Background(), s, q, 5); err != nil {
			t.Fatalf("encrypted skill search %q: %v", q, err)
		}
	}
	for _, q := range []string{"日本語", "スキル"} {
		got, err := skillstore.Search(context.Background(), s, q, 5)
		if err != nil || len(got) != 1 || got[0].Name != "日本語スキル" {
			t.Fatalf("encrypted CJK skill %q = %+v, %v", q, got, err)
		}
	}
}
