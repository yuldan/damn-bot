package wake

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func script(t *testing.T, dir, body string) string {
	t.Helper()
	f, err := os.CreateTemp(dir, "engine-*")
	if err != nil {
		t.Fatalf("create script: %v", err)
	}
	path := f.Name()
	if _, err := f.WriteString("#!/bin/sh\n" + body); err != nil {
		t.Fatalf("write script: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close script: %v", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatalf("chmod script: %v", err)
	}
	return path
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
	dir := t.TempDir()
	tests := []struct {
		name string
		body string
		want error
	}{
		{"wake", "exit 0\n", nil},
		{"silence", "exit 1\n", ErrTimeout},
		{"broken", "exit 7\n", ErrCommand},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := engine(t, Config{Enabled: true, Command: []string{script(t, dir, tc.body)}})
			if err := e.Wait(t.Context()); !errors.Is(err, tc.want) && !(err == nil && tc.want == nil) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestEngineTimeout(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	e := engine(t, Config{Enabled: true, Command: []string{script(t, dir, "exec sleep 5\n")}, Timeout: 100 * time.Millisecond})
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
	dir := t.TempDir()
	e := engine(t, Config{Enabled: true, Command: []string{filepath.Join(dir, "nope")}})
	if err := e.Wait(t.Context()); !errors.Is(err, ErrCommand) {
		t.Errorf("missing binary err = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e = engine(t, Config{Enabled: true, Command: []string{script(t, dir, "exit 0\n")}})
	if err := e.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled err = %v", err)
	}
}
