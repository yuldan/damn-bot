//go:build windows && !minimal

package exec

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestBackgroundWindowsFailsClosed pins the Windows capability declaration:
// Start refuses with ErrCapability (no Job Object) instead of detaching a
// process Close cannot kill, and the refusal leaves the registry empty.
func TestBackgroundWindowsFailsClosed(t *testing.T) {
	t.Parallel()
	if BackgroundSupported {
		t.Fatal("BackgroundSupported = true on Windows without a Job Object")
	}
	b := NewBackground()
	t.Cleanup(func() {
		if err := b.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	_, err := b.Start(context.Background(), "s", []string{"cmd", "/c", "exit 0"}, "", nil)
	if !errors.Is(err, ErrCapability) || !strings.Contains(err.Error(), "Job Object") {
		t.Fatalf("Start = %v, want ErrCapability naming the Job Object", err)
	}
	if got := b.List("s"); len(got) != 0 {
		t.Fatalf("registry after refusal = %+v", got)
	}
}
