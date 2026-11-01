//go:build unix

package exec

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestBackendContractLocal is the shared backend contract (EXEC-AND-CODING
// §A.7, EXC-16) for the local backend: spawn, exit code, timeout, tree kill,
// Sweep and Close.
//
//nolint:gocognit,gocyclo // one subtest per contract clause is the readable form
func TestBackendContractLocal(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	r := New(ctx)
	backend := NewLocal(r)
	t.Cleanup(func() {
		//nolint:errcheck,gosec // test cleanup
		backend.Close(context.Background())
	})

	if backend.ID() != "local" {
		t.Fatalf("ID = %q", backend.ID())
	}
	caps := backend.Caps()
	if !caps.PTY || !caps.Signals || !caps.SharesFS || !caps.Sessions || caps.ResourceLimits {
		t.Fatalf("Caps = %+v", caps)
	}

	t.Run("spawn and exit code", func(t *testing.T) {
		t.Parallel()
		res, err := backend.Run(ctx, &Spec{Argv: argv("exit 7"), Env: []string{"PATH=/bin:/usr/bin"}})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.Status != StatusExited || res.ExitCode == nil || *res.ExitCode != 7 {
			t.Fatalf("status = %s exit = %v", res.Status, res.ExitCode)
		}
	})

	t.Run("timeout kills the tree", func(t *testing.T) {
		t.Parallel()
		start := time.Now()
		res, err := backend.Run(ctx, &Spec{
			Argv: argv("sleep 30 & sleep 30"), Env: []string{"PATH=/bin:/usr/bin"},
			Timeout: 200 * time.Millisecond, KillGrace: time.Second,
		})
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if res.Status != StatusTimeout {
			t.Fatalf("status = %s", res.Status)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("timeout took %s", elapsed)
		}
		if killed, err := backend.Sweep(ctx, res.ExecID); err != nil || killed != 0 {
			t.Fatalf("Sweep = %d, %v", killed, err)
		}
	})

	t.Run("Sweep validates the id", func(t *testing.T) {
		t.Parallel()
		if _, err := backend.Sweep(ctx, ""); !errors.Is(err, ErrSpec) {
			t.Fatalf("Sweep(empty) = %v", err)
		}
	})

	t.Run("resource limits fail closed", func(t *testing.T) {
		t.Parallel()
		res, err := backend.Run(ctx, &Spec{Argv: argv("true"), Limits: Limits{MemoryMiB: 64}})
		if !errors.Is(err, ErrCapability) {
			t.Fatalf("Run = %v", err)
		}
		if res.Status != StatusSpawnFailed {
			t.Fatalf("status = %s", res.Status)
		}
	})

	if err := Require(backend, Caps{PTY: true, Sessions: true}); err != nil {
		t.Fatalf("Require: %v", err)
	}
	if err := Require(backend, Caps{ResourceLimits: true}); !errors.Is(err, ErrCapability) || !strings.Contains(err.Error(), "resource_limits") {
		t.Fatalf("Require(resource_limits) = %v", err)
	}
	if id := NewExecID(); id == "" || !strings.Contains(id, ".") {
		t.Fatalf("NewExecID = %q", id)
	}
}

// argv wraps a shell line in the platform's shell for the contract test.
func argv(line string) []string { return []string{"/bin/sh", "-c", line} }
