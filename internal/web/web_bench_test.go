package web

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// benchFirstDelta is the first token of the fake provider in BenchmarkWebFirstToken.
const benchFirstDelta = "halo dunia"

// firstToken is the fake provider seam of BenchmarkWebFirstToken: it returns
// the provider's first delta with no network or model latency, exactly like the
// TUI P17 harness fixes the headless TTFT with a fake runner. That keeps the
// measurement on the frontend path (SPEC §17: first token ≤ TTFT + 50 ms for
// the web UI), not on the provider.
func firstToken() <-chan string {
	ch := make(chan string, 1)
	ch <- benchFirstDelta
	return ch
}

// BenchmarkWebFirstToken is P17 (SPEC §17) for the web UI: after the fake
// provider emits its first token, the SSE client must see it no more than 50 ms
// after the terminal/chat baseline.
//
// How it is measured:
//   - The terminal/chat leg is the in-process path every frontend shares with
//     the loop: receive the fake provider's first delta from its channel.
//   - The web leg is the production SSE write path — writeFrame over a real
//     loopback net/http connection, followed by an SSE client read of the
//     first `data:` line. Nothing below net/http is mocked.
//   - Both legs consume the same fake-provider token, so the reported
//     overhead is median(web) − median(terminal). It covers request dispatch,
//     SSE framing and the flush that must reach the browser; it never includes
//     provider TTFT, which both legs share.
//
// The median of b.N samples keeps one scheduler hiccup from failing the gate or
// hiding a regression; a steady state is measured after ten warmup streams
// (first dial and buffer growth are setup, not per-token cost).
//
// Run: go test -run '^$' -bench '^BenchmarkWebFirstToken$' -benchtime=100x ./internal/web
func BenchmarkWebFirstToken(b *testing.B) {
	const budget = 50 * time.Millisecond
	tokens := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Accel-Buffering", "no")
		if err := writeFrame(w, flusher, "token", []byte(<-tokens)); err != nil {
			return
		}
	}))
	defer srv.Close()
	client := srv.Client()
	defer client.CloseIdleConnections()

	for range 10 {
		tokens <- benchFirstDelta
		readFirstToken(b, client, srv.URL)
	}

	web := make([]time.Duration, 0, b.N)
	terminal := make([]time.Duration, 0, b.N)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		ch := firstToken()
		start := time.Now()
		<-ch
		terminal = append(terminal, time.Since(start))

		tokens <- benchFirstDelta
		start = time.Now()
		readFirstToken(b, client, srv.URL)
		web = append(web, time.Since(start))
	}
	b.StopTimer()

	slices.Sort(web)
	slices.Sort(terminal)
	webMed, termMed := web[len(web)/2], terminal[len(terminal)/2]
	overhead := max(webMed-termMed, 0)
	b.ReportMetric(float64(webMed), "web-ns")
	b.ReportMetric(float64(termMed), "terminal-ns")
	b.ReportMetric(float64(overhead)/float64(time.Millisecond), "overhead-ms")
	if overhead > budget {
		b.Fatalf("P17 web first token overhead = %s (web %s, terminal %s, n=%d) > %s",
			overhead, webMed, termMed, b.N, budget)
	}
}

// readFirstToken opens one stream and reads the first event's data line.
func readFirstToken(b *testing.B, client *http.Client, url string) {
	b.Helper()
	req, err := http.NewRequestWithContext(b.Context(), http.MethodGet, url, nil)
	if err != nil {
		b.Fatalf("request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		b.Fatalf("stream: %v", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			b.Errorf("close stream: %v", cerr)
		}
	}()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		b.Fatalf("content-type = %q", ct)
	}
	br := bufio.NewReader(resp.Body)
	got := ""
	for {
		line, rerr := br.ReadString('\n')
		if rerr != nil {
			b.Fatalf("read first token: %v", rerr)
		}
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			got = strings.TrimSuffix(data, "\n")
		}
		if line == "\n" {
			break
		}
	}
	if got != benchFirstDelta {
		b.Fatalf("first token = %q, want %q", got, benchFirstDelta)
	}
}
