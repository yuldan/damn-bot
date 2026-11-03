package wake

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// shell returns an argv that runs body with the system shell, skipping on
// platforms without a POSIX shell fixture.
func shell(t *testing.T, body string) []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell fixture")
	}
	return []string{"/bin/sh", "-c", body}
}

func engine(t *testing.T, cfg Config) *External {
	t.Helper()
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return e
}

func TestDisabledByDefault(t *testing.T) {
	t.Parallel()
	e := engine(t, Config{})
	if e.Enabled() {
		t.Fatal("engine enabled without opt-in")
	}
	if err := e.Wait(t.Context()); !errors.Is(err, ErrDisabled) {
		t.Fatalf("err = %v, want ErrDisabled", err)
	}
}

func TestEnabledNeedsCommand(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{Enabled: true}); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
}

func TestExitStatusContract(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		body string
		want error
	}{
		{"wake", "exit 0", nil},
		{"silence", "exit 1", ErrTimeout},
		{"broken", "exit 7", ErrCommand},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := engine(t, Config{Enabled: true, Command: shell(t, tc.body)})
			err := e.Wait(t.Context())
			if tc.want == nil && err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestEngineTimeout(t *testing.T) {
	t.Parallel()
	e := engine(t, Config{Enabled: true, Command: shell(t, "exec sleep 5"), Timeout: 100 * time.Millisecond})
	start := time.Now()
	if err := e.Wait(t.Context()); !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("timeout took %s", elapsed)
	}
}

func TestMissingAndCanceled(t *testing.T) {
	t.Parallel()
	e := engine(t, Config{Enabled: true, Command: []string{filepath.Join(t.TempDir(), "nope")}})
	if err := e.Wait(t.Context()); !errors.Is(err, ErrCommand) {
		t.Errorf("missing binary err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e = engine(t, Config{Enabled: true, Command: shell(t, "exit 0")})
	if err := e.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled err = %v", err)
	}
}

// TestF6MEDIA003WakeWord: the external engine is off by default and only the
// explicit opt-in runs a command (exit 0 = wake, 1 = silence).
func TestF6MEDIA003WakeWord(t *testing.T) {
	t.Parallel()
	e := engine(t, Config{})
	if e.Enabled() {
		t.Fatal("engine enabled without opt-in")
	}
	if err := e.Wait(t.Context()); !errors.Is(err, ErrDisabled) {
		t.Fatalf("err = %v", err)
	}
	e = engine(t, Config{Enabled: true, Command: shell(t, "exit 0")})
	if err := e.Wait(t.Context()); err != nil {
		t.Fatalf("wake err = %v", err)
	}
}
