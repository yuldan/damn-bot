//go:build unix

package exec

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCancelConfirmed (GAP-REL-06, P24, ADR-072): a cancelled tool that has
// not confirmed cannot have written after the caller received the timeout
// result. Each iteration arms a delayed write; once Run returns, the file
// must never appear.
func TestCancelConfirmed(t *testing.T) {
	t.Parallel()
	r := newTestRunner(t)
	for i := range 20 {
		dir := t.TempDir()
		out := filepath.Join(dir, "late.txt")
		s := sh(t, "sleep 0.3; echo late > late.txt")
		s.Dir = dir
		s.Timeout = 50 * time.Millisecond
		s.KillGrace = time.Second
		res := run(t.Context(), t, r, s)
		if res.Status != StatusTimeout && res.Status != StatusCancelledConfirmed && res.Status != StatusStillRunning {
			t.Fatalf("iteration %d: status %q, want a cancel/timeout result", i, res.Status)
		}
		if res.Done != nil {
			<-res.Done
		}
		select {
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		case <-time.After(400 * time.Millisecond):
		}
		if _, err := os.Stat(out); err == nil {
			t.Fatalf("iteration %d: cancelled tool wrote %s after returning %q", i, out, res.Status)
		}
	}
}
