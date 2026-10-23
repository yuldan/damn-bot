//go:build perf

// Recall acceptance harness (SPEC §11.6, §17): 1.000.000 events, p95 ≤ 50 ms.
// The corpus is built once per benchmark/test and never in the normal lane;
// run it with `go test -tags goolm,perf -run TestRecallP95 ./internal/ctxdb`.
// DAMNBOT_RECALL_EVENTS overrides the corpus size (default 1.000.000); the
// reference machine is Q14 (x86-64 4 core, NVMe, 16 GB RAM, Linux 6.x).
package ctxdb

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"
)

const defaultRecallEvents = 1_000_000

func recallCorpus(tb testing.TB, n int) *Store {
	tb.Helper()
	s, err := Open(context.Background(), &Options{Dir: tb.TempDir(), Kind: "run"})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			tb.Error(cerr)
		}
	})
	ctx := context.Background()
	if _, err := s.CreateSession(ctx, Session{ID: "S", Origin: "headless", Cwd: "/"}); err != nil {
		tb.Fatal(err)
	}
	// Batches of 50.000 rows: one recursive CTE per transaction, trigger-fed
	// FTS, no per-row round trips. Content is a synthetic three-term line so
	// a query matches roughly one vocabulary bucket.
	const batch = 50_000
	for start := 1; start <= n; start += batch {
		end := min(start+batch-1, n)
		err := s.Write(ctx, func(tx *Tx) error {
			_, xerr := tx.Exec(`WITH RECURSIVE c(i) AS (SELECT ?1 UNION ALL SELECT i + 1 FROM c WHERE i < ?2)
				INSERT INTO events(session_id, seq, kind, role, content, payload, prev_hash, hash, created_at)
				SELECT 'S', i, 'user_message', 'user',
				       'event ' || i || ' w' || (i % 97) || ' w' || ((i * 7) % 997) || ' w' || ((i * 13) % 991),
				       '{}', 'p', 'h' || i, 1
				FROM c`, start, end)
			return xerr
		})
		if err != nil {
			tb.Fatalf("corpus %d..%d: %v", start, end, err)
		}
	}
	return s
}

func recallCorpusSize(tb testing.TB) int {
	tb.Helper()
	v := os.Getenv("DAMNBOT_RECALL_EVENTS")
	if v == "" {
		return defaultRecallEvents
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		tb.Fatalf("DAMNBOT_RECALL_EVENTS=%q", v)
	}
	return n
}

func percentile(lat []time.Duration, p int) time.Duration {
	if len(lat) == 0 {
		return 0
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	idx := (len(lat)*p+99)/100 - 1
	if idx < 0 {
		idx = 0
	}
	return lat[idx]
}

// TestRecallP95 is the acceptance measurement: 200 mixed queries over the
// corpus, p95 ≤ 50 ms (SPEC §11.6).
func TestRecallP95(t *testing.T) {
	n := recallCorpusSize(t)
	t.Logf("building %d events", n)
	s := recallCorpus(t, n)
	ctx := context.Background()
	for _, c := range []struct {
		name string
		q    func(i int) string
	}{
		{"selective", func(i int) string { return fmt.Sprintf("w%d", i%97) }},
		{"broad", func(int) string { return "event" }},
	} {
		const samples = 200
		lat := make([]time.Duration, samples)
		for i := range samples {
			start := time.Now()
			if _, err := s.Recall(ctx, c.q(i), 10); err != nil {
				t.Fatal(err)
			}
			lat[i] = time.Since(start)
		}
		p50, p95 := percentile(lat, 50), percentile(lat, 95)
		t.Logf("recall %s @%d events: p50=%s p95=%s", c.name, n, p50, p95)
		if p95 > 50*time.Millisecond {
			t.Fatalf("%s p95 = %s > 50 ms (SPEC §11.6, Q14)", c.name, p95)
		}
	}
}

// TestRecallP95Broad100k covers the reviewer's broad-term case at 100k
// (JN3-6); the broad fallback must stay under 50 ms.
func TestRecallP95Broad100k(t *testing.T) {
	const n = 100_000
	s := recallCorpus(t, n)
	ctx := context.Background()
	const samples = 100
	lat := make([]time.Duration, samples)
	for i := range samples {
		start := time.Now()
		if _, err := s.Recall(ctx, "event", 50); err != nil {
			t.Fatal(err)
		}
		lat[i] = time.Since(start)
	}
	p95 := percentile(lat, 95)
	t.Logf("broad recall @%d events: p95=%s", n, p95)
	if p95 > 50*time.Millisecond {
		t.Fatalf("broad p95 = %s > 50 ms (JN3-6)", p95)
	}
}

// BenchmarkRecall reports ns/op plus p50/p95 metrics over the same corpus;
// -benchtime=1x is enough (setup dominates).
func BenchmarkRecall(b *testing.B) {
	n := recallCorpusSize(b)
	b.Logf("building %d events", n)
	s := recallCorpus(b, n)
	ctx := context.Background()
	const keep = 1000
	lat := make([]time.Duration, 0, keep)
	i := 0
	for b.Loop() {
		start := time.Now()
		if _, err := s.Recall(ctx, fmt.Sprintf("w%d", i%97), 10); err != nil {
			b.Fatal(err)
		}
		if len(lat) < keep {
			lat = append(lat, time.Since(start))
		} else {
			lat[i%keep] = time.Since(start)
		}
		i++
	}
	b.StopTimer()
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	b.ReportMetric(ms(percentile(lat, 50)), "p50-ms")
	b.ReportMetric(ms(percentile(lat, 95)), "p95-ms")
	b.ReportMetric(float64(n), "events")
}

// TestRecallP95Purged100k: 10 visible events older than 100k purge-labelled
// events; SQL-level visibility keeps k=10/50 under 50 ms without OFFSET
// paging or starvation (JN3-12/JN3-14).
func TestRecallP95Purged100k(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, &Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	for _, id := range []string{"B", "P"} {
		if _, err = s.CreateSession(ctx, Session{ID: id, Origin: "headless", Cwd: "/"}); err != nil {
			t.Fatal(err)
		}
	}
	valid := make([]Event, 10)
	for i := range valid {
		valid[i] = Event{Kind: "user_message", Role: "user", Content: "pageword valid"}
	}
	if _, err = s.Append(ctx, "B", nil, valid...); err != nil {
		t.Fatal(err)
	}
	const hidden = 100_000
	for start := 1; start <= hidden; start += 50_000 {
		end := min(start+50_000-1, hidden)
		err = s.Write(ctx, func(tx *Tx) error {
			_, xerr := tx.Exec(`WITH RECURSIVE c(i) AS (SELECT ?1 UNION ALL SELECT i + 1 FROM c WHERE i < ?2)
				INSERT INTO events(session_id, seq, kind, role, content, payload, prev_hash, hash, created_at)
				SELECT 'P', i, 'user_message', 'user', 'pageword hidden', '{}', 'p', 'h' || i, 1 FROM c`,
				start, end)
			return xerr
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = s.tx(ctx, modePurge, func(tx *Tx) error {
		_, xerr := tx.Exec(`INSERT INTO session_labels(session_id, key, value, created_at)
			VALUES ('P', 'purge_state', 'tx-test', 1)`)
		return xerr
	}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []int{10, 50} {
		const samples = 50
		lat := make([]time.Duration, samples)
		for i := range samples {
			start := time.Now()
			got, gerr := s.Recall(ctx, "pageword", k)
			if gerr != nil {
				t.Fatal(gerr)
			}
			lat[i] = time.Since(start)
			if len(got) != 10 {
				t.Fatalf("k=%d: %d hits, want 10", k, len(got))
			}
			for _, h := range got {
				if h.SessionID != "B" {
					t.Fatalf("k=%d: purged hit %+v", k, h)
				}
			}
		}
		p95 := percentile(lat, 95)
		t.Logf("purged-100k recall k=%d: p95=%s", k, p95)
		if p95 > 50*time.Millisecond {
			t.Fatalf("k=%d p95 = %s > 50 ms (JN3-14)", k, p95)
		}
	}
}

// cjkRecallCorpus builds CJK content at the same scale as recallCorpus.
func cjkRecallCorpus(tb testing.TB, n int) *Store {
	tb.Helper()
	s, err := Open(context.Background(), &Options{Dir: tb.TempDir(), Kind: "run"})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			tb.Error(cerr)
		}
	})
	ctx := context.Background()
	if _, err := s.CreateSession(ctx, Session{ID: "S", Origin: "headless", Cwd: "/"}); err != nil {
		tb.Fatal(err)
	}
	const batch = 50_000
	for start := 1; start <= n; start += batch {
		end := min(start+batch-1, n)
		err := s.Write(ctx, func(tx *Tx) error {
			_, xerr := tx.Exec(`WITH RECURSIVE c(i) AS (SELECT ?1 UNION ALL SELECT i + 1 FROM c WHERE i < ?2)
				INSERT INTO events(session_id, seq, kind, role, content, payload, prev_hash, hash, created_at)
				SELECT 'S', i, 'user_message', 'user',
				       '天气预报 广西壮族 桂林山水 股票行情 ' || i, '{}', 'p', 'h' || i, 1 FROM c`, start, end)
			return xerr
		})
		if err != nil {
			tb.Fatal(err)
		}
	}
	return s
}

// BenchmarkRecallCJK (GAP-INF-01): CJK query at the acceptance corpus size.
func BenchmarkRecallCJK(b *testing.B) {
	n := recallCorpusSize(b)
	s := cjkRecallCorpus(b, n)
	ctx := context.Background()
	b.ResetTimer()
	for range b.N {
		if _, err := s.Recall(ctx, "天气预报", 10); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRecallToolOutput (GAP-INF-02): tool-result recall on 100k rows.
func BenchmarkRecallToolOutput(b *testing.B) {
	const n = 100_000
	s, err := Open(context.Background(), &Options{Dir: b.TempDir(), Kind: "run"})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			b.Error(cerr)
		}
	})
	ctx := context.Background()
	if _, err := s.CreateSession(ctx, Session{ID: "S", Origin: "headless", Cwd: "/"}); err != nil {
		b.Fatal(err)
	}
	const batch = 50_000
	for start := 1; start <= n; start += batch {
		end := min(start+batch-1, n)
		err := s.Write(ctx, func(tx *Tx) error {
			_, xerr := tx.Exec(`WITH RECURSIVE c(i) AS (SELECT ?1 UNION ALL SELECT i + 1 FROM c WHERE i < ?2)
				INSERT INTO events(session_id, seq, kind, role, content, payload, prev_hash, hash, created_at)
				SELECT 'S', i, 'tool_result', 'tool', 'tool output line ' || i, '{}', 'p', 'h' || i, 1 FROM c`, start, end)
			return xerr
		})
		if err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for range b.N {
		if _, err := s.Recall(ctx, "tool output", 10); err != nil {
			b.Fatal(err)
		}
	}
}
