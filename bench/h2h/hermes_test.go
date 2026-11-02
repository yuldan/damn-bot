package h2h

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestHermesRunStopReason: proses hermes yang selesai normal harus mengisi kontrak stop
// (end_turn/completed), exit bukan nol = error, dan deadline = timeout. Sebelum fix,
// ketiganya kosong sehingga categorizeRun menyebutnya upstream_unavailable dan retry 2×.
func TestHermesRunStopReason(t *testing.T) {
	t.Parallel()
	script := func(t *testing.T, body string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "fake-python")
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct {
		name       string
		body       string
		timeout    time.Duration
		wantReason string
		wantDetail string
		wantTimed  bool
	}{
		{"success", "exit 0\n", 10 * time.Second, "end_turn", "completed", false},
		{"nonzero", "exit 3\n", 10 * time.Second, "error", "", false},
		{"timeout", "exec sleep 30\n", 200 * time.Millisecond, "timeout", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h := &Hermes{
				Dir: t.TempDir(), Home: filepath.Join(t.TempDir(), "home"),
				Python: script(t, c.body), Model: "test-model", MaxTurns: 1,
			}
			run, err := h.Run(context.Background(), t.TempDir(), "hi", "http://127.0.0.1:1/v1", c.timeout)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if run.TimedOut != c.wantTimed {
				t.Fatalf("TimedOut = %v, want %v", run.TimedOut, c.wantTimed)
			}
			if run.StopReason != c.wantReason || run.StopDetail != c.wantDetail {
				t.Fatalf("stop = %q/%q, want %q/%q", run.StopReason, run.StopDetail, c.wantReason, c.wantDetail)
			}
			category := categorizeRun(run, Usage{}, nil)
			if c.name == "success" && (category != "ok" || retryable(category)) {
				t.Fatalf("success category = %q retryable=%v, want ok/not retried", category, retryable(category))
			}
		})
	}
}
