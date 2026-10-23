package ctxdb

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"
)

func benchRecallCorpus(b *testing.B, codec Codec, n int) *Store {
	b.Helper()
	s, err := Open(context.Background(), &Options{Dir: b.TempDir(), Kind: "run", Codec: codec})
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
	if err := s.Write(ctx, func(tx *Tx) error {
		for i := 1; i <= n; i++ {
			content := fmt.Sprintf("event %d w%d w%d", i, i%97, (i*7)%997)
			stored, enc := content, []byte(nil)
			if codec != nil {
				var serr error
				enc, stored, serr = tx.SealText("events.content", content, "")
				if serr != nil {
					return serr
				}
			}
			if _, xerr := tx.Exec(`INSERT INTO events(session_id,seq,kind,role,content,content_enc,payload,prev_hash,hash,created_at)
				VALUES ('S',?, 'user_message','user',?,?, '{}', 'p', ?, 1)`, i, stored, nullB(enc), fmt.Sprintf("h%d", i)); xerr != nil {
				return xerr
			}
		}
		return nil
	}); err != nil {
		b.Fatalf("corpus: %v", err)
	}
	return s
}

func nullB(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func benchRecallN() int {
	if v := os.Getenv("DAMNBOT_RECALL_EVENTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 100_000
}

func benchRecall(b *testing.B, codec Codec) {
	s := benchRecallCorpus(b, codec, benchRecallN())
	ctx := context.Background()
	b.ResetTimer()
	lat := make([]time.Duration, 0, b.N)
	for i := range b.N {
		start := time.Now()
		if _, err := s.Recall(ctx, fmt.Sprintf("w%d", i%97), 10); err != nil {
			b.Fatal(err)
		}
		lat = append(lat, time.Since(start))
	}
	b.StopTimer()
	reportLatency(b, lat)
}

func BenchmarkRecallPlain100k(b *testing.B)      { benchRecall(b, nil) }
func BenchmarkRecallBlindIndex100k(b *testing.B) { benchRecall(b, newBenchCodec(b)) }
