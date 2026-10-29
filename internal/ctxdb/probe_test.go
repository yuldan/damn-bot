package ctxdb

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProbeWriteHealthy(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s := e.open(1, "serve")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.ProbeWrite(ctx); err != nil {
		t.Fatalf("ProbeWrite: %v", err)
	}
}

func TestProbeWriteBlocked(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	holder := e.open(1, "cli")
	probe := e.open(2, "serve")
	release, done := holdWriteLock(context.Background(), t, holder)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	err := probe.ProbeWrite(ctx)
	elapsed := time.Since(start)
	release()
	if werr := <-done; werr != nil {
		t.Fatalf("holder write: %v", werr)
	}
	if !errors.Is(err, ErrDBBusy) {
		t.Fatalf("ProbeWrite while locked = %v, want ErrDBBusy", err)
	}
	if elapsed > time.Second {
		t.Fatalf("ProbeWrite took %s, want the 250 ms probe bound", elapsed)
	}
}

func TestProbeWriteReadOnly(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.open(1, "cli")
	ro := e.open(2, "doctor", func(o *Options) { o.ReadOnly = true })
	if err := ro.ProbeWrite(context.Background()); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only ProbeWrite = %v, want ErrReadOnly", err)
	}
}

// TestProbeWriteLeavesNoWriterState proves the rollback path: after a probe,
// a normal write still commits and the history is intact.
func TestProbeWriteLeavesNoWriterState(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s := e.open(1, "serve")
	if err := s.ProbeWrite(context.Background()); err != nil {
		t.Fatalf("ProbeWrite: %v", err)
	}
	if err := s.Write(context.Background(), func(*Tx) error { return nil }); err != nil {
		t.Fatalf("write after probe: %v", err)
	}
	checkIntegrity(t, s)
}
