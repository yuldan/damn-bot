//go:build windows && !minimal

package exec

import (
	"context"
	"errors"
	"testing"
)

// TestBackgroundWindowsFailsClosed pins the Windows capability declaration:
// Start refuses (no Job Object) instead of detaching a process Close cannot
// kill, and the refusal leaves the registry empty.
func TestBackgroundWindowsFailsClosed(t *testing.T) {
	t.Parallel()
	b := NewBackground()
	t.Cleanup(func() {
		if err := b.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	if _, err := b.Start(context.Background(), "s", []string{"cmd", "/c", "exit 0"}, "", nil); !errors.Is(err, ErrProcessLimit) {
		t.Fatalf("Start = %v, want ErrProcessLimit", err)
	}
	if got := b.List("s"); len(got) != 0 {
		t.Fatalf("registry after refusal = %+v", got)
	}
}
