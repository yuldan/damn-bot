package ctxdb

import (
	"bytes"
	"context"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/chacha20poly1305"
)

// Benchmarks of the F2 Append targets (ROADMAP F2, SPEC §4.6): Append ≤ 10 ms
// p99 (30 ms macOS) and AppendDuringGC p99 ≤ 210 ms. They need no corpus and
// always run under `make perf`. Recall and its 1M harness are owned by PR
// #152 (`recall_perf_test.go`), not duplicated here.

func percentiles(ds []time.Duration) (p50, p95, p99 time.Duration) {
	if len(ds) == 0 {
		return 0, 0, 0
	}
	slices.Sort(ds)
	at := func(p float64) time.Duration {
		i := int(math.Ceil(p*float64(len(ds)))) - 1
		return ds[max(0, min(i, len(ds)-1))]
	}
	return at(0.50), at(0.95), at(0.99)
}

func reportLatency(b *testing.B, ds []time.Duration) {
	p50, p95, p99 := percentiles(ds)
	ms := float64(time.Millisecond)
	b.ReportMetric(float64(p50)/ms, "p50-ms")
	b.ReportMetric(float64(p95)/ms, "p95-ms")
	b.ReportMetric(float64(p99)/ms, "p99-ms")
}

func benchStore(b testing.TB, kind string) *Store {
	b.Helper()
	s, err := Open(context.Background(), &Options{Dir: b.TempDir(), Kind: kind})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := s.Close(); err != nil {
			b.Errorf("close ctxdb: %v", err)
		}
	})
	return s
}

func benchMust(b testing.TB, err error) {
	b.Helper()
	if err != nil {
		b.Fatal(err)
	}
}

// benchSession creates one leased session whose events carry 3 blob refs
// (ADR-039: the blob_refs insert is part of the Append budget).
func benchSession(b testing.TB, s *Store, sid string) (blob, refA, refB string) {
	b.Helper()
	ctx := context.Background()
	benchMust(b, s.Write(ctx, func(tx *Tx) error {
		_, err := tx.CreateSession(Session{ID: sid, Origin: "headless", Cwd: "/bench"})
		return err
	}))
	p0, err := s.PutBlob([]byte("bench blob zero"), "text/plain")
	benchMust(b, err)
	p1, err := s.PutBlob([]byte("bench blob one"), "text/plain")
	benchMust(b, err)
	p2, err := s.PutBlob([]byte("bench blob two"), "text/plain")
	benchMust(b, err)
	payload := fmt.Sprintf(`{"a_blob_sha":%q,"b_blob_sha":%q}`, p1.Sha, p2.Sha)
	_, err = s.Append(ctx, sid, []*PendingBlob{p0, p1, p2},
		Event{Kind: "system_notice", Payload: payload, Blob: p0.Sha})
	benchMust(b, err)
	return p0.Sha, p1.Sha, p2.Sha
}

func benchPayload(blob, refA, refB string) Event {
	return Event{
		Kind: "tool_result", Role: "tool", Content: "benchmark tool result content",
		Payload: fmt.Sprintf(`{"a_blob_sha":%q,"b_blob_sha":%q}`, refA, refB), Blob: blob,
	}
}

// BenchmarkAppend is Store.Append on the hot path: synchronous=FULL, one
// hash-chained event + 3 blob_refs per iteration.
func BenchmarkAppend(b *testing.B) {
	s := benchStore(b, "run")
	ctx := context.Background()
	blob, refA, refB := benchSession(b, s, "bench")
	ev := benchPayload(blob, refA, refB)
	ds := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := range b.N {
		ev.Content = "append sample " + strconv.Itoa(i)
		start := time.Now()
		_, err := s.Append(ctx, "bench", nil, ev)
		ds = append(ds, time.Since(start))
		if err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	reportLatency(b, ds)
}

// BenchmarkAppendDuringGC appends while GC runs on another goroutine. GC
// takes the writer lock in ≤200 ms batches with a ≤20 % duty cycle, so p99
// may include one batch wait (target ≤ 210 ms).
func BenchmarkAppendDuringGC(b *testing.B) {
	s := benchStore(b, "run")
	ctx := context.Background()
	blob, refA, refB := benchSession(b, s, "bench")
	// Orphan blobs are GC candidates: 500 per batch, many batches.
	benchMust(b, s.Write(ctx, func(tx *Tx) error {
		for i := range 5_000 {
			p, err := s.PutBlob([]byte(fmt.Sprintf("orphan-%d", i)), "text/plain")
			if err != nil {
				return err
			}
			if err := tx.Publish(p); err != nil {
				return err
			}
		}
		return nil
	}))
	gctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for gctx.Err() == nil {
			if _, err := s.GC(gctx, false); err != nil {
				return
			}
		}
	}()
	ev := benchPayload(blob, refA, refB)
	ds := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := range b.N {
		ev.Content = "append during gc " + strconv.Itoa(i)
		start := time.Now()
		_, err := s.Append(ctx, "bench", nil, ev)
		ds = append(ds, time.Since(start))
		if err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	stop()
	<-done
	reportLatency(b, ds)
}

type benchCodec struct {
	aead cipher.AEAD
}

func newBenchCodec(b *testing.B) *benchCodec {
	b.Helper()
	a, err := chacha20poly1305.NewX(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		b.Fatal(err)
	}
	return &benchCodec{aead: a}
}

func (c *benchCodec) ID() string { return "bench" }
func (c *benchCodec) Seal(scope string, p []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("bench nonce: %w", err)
	}
	out := make([]byte, 1, 1+len(nonce)+len(p)+c.aead.Overhead())
	out[0] = 1
	out = append(out, nonce...)
	return c.aead.Seal(out, nonce, p, []byte(scope)), nil
}

func (c *benchCodec) FTS(tokens string) string {
	key := bytes.Repeat([]byte{9}, 32)
	var out []string
	for tok := range strings.FieldsSeq(tokens) {
		m := hmac.New(sha256.New, key)
		m.Write([]byte(tok))
		out = append(out, hex.EncodeToString(m.Sum(nil)))
	}
	return strings.Join(out, " ")
}

func (c *benchCodec) Hash(s string) string { return s }

func (c *benchCodec) Open(scope string, b []byte) ([]byte, error) {
	n := c.aead.NonceSize()
	out, err := c.aead.Open(nil, b[1:1+n], b[1+n:], []byte(scope))
	if err != nil {
		return nil, fmt.Errorf("bench codec: %w", err)
	}
	return out, nil
}

// BenchmarkAppendEncrypted is BenchmarkAppend with a real XChaCha20-Poly1305
// codec for event content/payload and blobs.
func BenchmarkAppendEncrypted(b *testing.B) {
	codec := newBenchCodec(b)
	s, err := Open(context.Background(), &Options{Dir: b.TempDir(), Kind: "run", Codec: codec})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			b.Errorf("close ctxdb: %v", cerr)
		}
	})
	ctx := context.Background()
	blob, refA, refB := benchSession(b, s, "bench")
	ev := benchPayload(blob, refA, refB)
	ds := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := range b.N {
		ev.Content = "append sample " + strconv.Itoa(i)
		start := time.Now()
		_, aerr := s.Append(ctx, "bench", nil, ev)
		ds = append(ds, time.Since(start))
		if aerr != nil {
			b.Fatal(aerr)
		}
	}
	b.StopTimer()
	reportLatency(b, ds)
}
