//go:build linux

package loop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// "EC-TERM-009" (EDGE-CASES-hermes.md): a gateway that leaks one descriptor
// per turn ends in EMFILE and becomes a zombie. One thousand complete turns
// through the real loop with a fake provider and a file-backed spiller must
// leave the descriptor table flat; the negative control proves a single
// leaked descriptor is caught.
const ecTerm009Cycles = 1000

// fdSlack covers one-off descriptors the Go runtime and the test harness open
// while the soak runs; a per-turn leak is orders of magnitude above it.
const fdSlack = 8

func TestECTERM009TurnSoakKeepsFDsFlat(t *testing.T) {
	dir := t.TempDir()
	total, leaked := fdStats(t, dir)
	if leaked != 0 {
		t.Fatalf("fresh soak dir already has %d open descriptors", leaked)
	}
	spill := &fileSpiller{dir: dir}
	for range ecTerm009Cycles {
		f := newFixture()
		f.cfg.MaxInline = 64
		f.spiller = spill
		f.provider.scripts = []streamFunc{
			callScript(FinishToolCalls, ToolCall{ID: "c", Name: "dump", Arguments: `{}`}),
			textScript("done", event.Usage{}),
		}
		f.tools.add("dump", &toolStub{dispatch: func(context.Context, *Prepared) (ToolResult, error) {
			return ToolResult{Content: strings.Repeat("x", 256)}, nil
		}})
		f.run(t)
	}
	if got := spill.calls.Load(); got != ecTerm009Cycles {
		t.Fatalf("spill calls = %d, want one per turn", got)
	}
	totalAfter, leakedAfter := fdStats(t, dir)
	if reason := fdSoakViolation(totalAfter-total, leakedAfter); reason != "" {
		t.Fatalf("after %d turns: %s", ecTerm009Cycles, reason)
	}
}

// TestECTERM009LeakedFDNegativeControl is the control for the soak check: one
// descriptor deliberately kept open under the spill dir must fail it.
func TestECTERM009LeakedFDNegativeControl(t *testing.T) {
	dir := t.TempDir()
	_, before := fdStats(t, dir)
	f, err := os.CreateTemp(dir, "leak-*")
	if err != nil {
		t.Fatalf("create leak: %v", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("close leak: %v", cerr)
		}
	}()
	_, after := fdStats(t, dir)
	if reason := fdSoakViolation(0, after-before); reason == "" {
		t.Fatal("one deliberately leaked descriptor went undetected")
	}
}

// fdStats returns the total descriptor count and how many point under dir.
func fdStats(t *testing.T, dir string) (total, leaked int) {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("read /proc/self/fd: %v", err)
	}
	for _, e := range entries {
		total++
		if link, lerr := os.Readlink("/proc/self/fd/" + e.Name()); lerr == nil && strings.HasPrefix(link, dir) {
			leaked++
		}
	}
	return total, leaked
}

// fdSoakViolation explains why a soak failed, or returns "" when it passed.
// Any descriptor left under the soak dir is a leak; the total may move by the
// runtime's one-off slack but never linearly with the turn count.
func fdSoakViolation(totalGrowth, leaked int) string {
	switch {
	case leaked > 0:
		return fmt.Sprintf("%d descriptor(s) leaked under the soak dir", leaked)
	case totalGrowth > fdSlack:
		return fmt.Sprintf("descriptor count grew by %d (> %d)", totalGrowth, fdSlack)
	}
	return ""
}

// fileSpiller performs real open/write/close I/O per spilled result, so the
// soak exercises descriptor lifecycle instead of a no-op fake.
type fileSpiller struct {
	dir   string
	calls atomic.Int64
}

func (s *fileSpiller) Spill(ctx context.Context, content string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("spill cancelled: %w", err)
	}
	s.calls.Add(1)
	f, err := os.CreateTemp(s.dir, "spill-*")
	if err != nil {
		return "", fmt.Errorf("create spill: %w", err)
	}
	if _, err := f.WriteString(content); err != nil {
		return "", fmt.Errorf("write spill: %w", errors.Join(err, f.Close()))
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close spill: %w", err)
	}
	return "file://" + f.Name(), nil
}
