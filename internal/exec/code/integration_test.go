//go:build unix

package code

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/damnbot/damnbot/internal/exec"
)

// realBackend delegates to the local runner but claims the network
// capability, so the integration test exercises the real argv path.
// Network deny itself is tested negatively in code_test.go; production
// local has Caps.Network=false and is refused.
type realBackend struct{ l *exec.Local }

func (r realBackend) ID() string      { return "local-harness" }
func (r realBackend) Caps() exec.Caps { return exec.Caps{Signals: true, Network: true, SharesFS: true} }

func (r realBackend) Run(ctx context.Context, s *exec.Spec) (exec.Result, error) {
	res, err := r.l.Run(ctx, s)
	if err != nil {
		return res, fmt.Errorf("test local run: %w", err)
	}
	return res, nil
}

func (r realBackend) Sweep(ctx context.Context, id string) (int, error) {
	n, err := r.l.Sweep(ctx, id)
	if err != nil {
		return n, fmt.Errorf("test local sweep: %w", err)
	}
	return n, nil
}

func (r realBackend) Close(ctx context.Context) error {
	if err := r.l.Close(ctx); err != nil {
		return fmt.Errorf("test local close: %w", err)
	}
	return nil
}

// TestRunThroughLocalBackendSpawnsStubInterpreter runs the real local backend
// with Config.Lookup injected: no host language interpreter (python3/node/go)
// is required, so the test is hermetic on every runner. The stub interpreter
// is a script that echoes stdin, proving argv, stdin, output, and cleanup.
func TestRunThroughLocalBackendSpawnsStubInterpreter(t *testing.T) {
	dir := t.TempDir()
	interp := filepath.Join(dir, "interp")
	if err := os.WriteFile(interp, []byte("#!/bin/sh\ncat\n"), 0o700); err != nil { // #nosec G306 -- fixture must be executable
		t.Fatal(err)
	}
	l := exec.NewLocal(exec.New(t.Context()))
	t.Cleanup(func() {
		if cerr := l.Close(context.Background()); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	r, err := New(Config{
		Backend: realBackend{l: l},
		Env:     []string{"PATH=/usr/bin:/bin"},
		Lookup: func(Language, []string) ([]string, error) {
			return []string{interp}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.Run(t.Context(), &Request{Language: Python, Code: "ignored", Stdin: []byte(`{"n":41}`)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got, want := string(res.Stdout), `{"n":41}`; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if res.Isolation.Network != "deny" {
		t.Fatalf("isolation = %+v", res.Isolation)
	}
}
