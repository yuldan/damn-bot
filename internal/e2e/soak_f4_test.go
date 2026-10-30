//go:build unix

package e2e

import (
	"context"
	"os"
	"runtime"
	"testing"

	"github.com/damnbot/damnbot/internal/boundary"
)

// TestSoakF4Resources (REL-12): 1.000 cron runs, 1.000 gateway turns and 200
// MCP reconnects each own a boundary.Scope with a pipe; after every scope
// closes, FD growth is ≤2 and no goroutine is left behind.
func TestSoakF4Resources(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("no /proc/self/fd")
	}
	beforeFD := openFDs(t)
	beforeG := settleGoroutines()
	for i := range 1000 {
		runScoped(t, "cron", i)
	}
	for i := range 1000 {
		runScoped(t, "gateway-turn", i)
	}
	for i := range 200 {
		runScoped(t, "mcp-reconnect", i)
	}
	afterFD := openFDs(t)
	afterG := settleGoroutines()
	if afterFD > beforeFD+2 {
		t.Fatalf("FD leak: %d -> %d", beforeFD, afterFD)
	}
	if afterG != beforeG {
		t.Fatalf("goroutine leak: %d -> %d", beforeG, afterG)
	}
}

func runScoped(t *testing.T, kind string, i int) {
	t.Helper()
	sc, ctx := boundary.NewScope(t.Context())
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.Add(r); err != nil {
		t.Fatal(err)
	}
	if err := sc.Add(w); err != nil {
		t.Fatal(err)
	}
	task := boundary.Go(ctx, kind, func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	})
	if err := sc.Close(); err != nil {
		t.Fatalf("%s %d: close: %v", kind, i, err)
	}
	if err := task.Wait(); err != nil {
		t.Fatalf("%s %d: %v", kind, i, err)
	}
}

func openFDs(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// settleGoroutines lets finished goroutines disappear before the census.
func settleGoroutines() int {
	for range 50 {
		runtime.GC()
		runtime.Gosched()
	}
	return runtime.NumGoroutine()
}
