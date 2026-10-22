package ctxdb

import (
	"os"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestProbeTimeout (EC-TERM-007): no process probe may wait unboundedly. The
// hermes bug was a kernel create_time probe that stalled for almost an hour
// on macOS; every probe here (absent and live pid, self identity) must return
// within a fixed deadline, and none has a retry or wait loop.
func TestProbeTimeout(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-TERM-007", "F1", "covered") {
		return
	}
	t.Run("EC-TERM-007", func(t *testing.T) {
		t.Parallel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			for _, pid := range []int{os.Getpid(), 0x7fffffff} {
				_ = (SystemProcs{}).Gone(Ident{Host: "h", PID: pid})
				_ = (SystemProcs{}).Gone(Ident{Host: "h", PID: pid, Start: "stamp"})
			}
			_ = SelfIdent()
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("a process probe did not return within 5 s (EC-TERM-007)")
		}
	})
}
