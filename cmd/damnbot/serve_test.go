package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

func TestServeRejectsNonLoopbackBind(t *testing.T) {
	t.Parallel()
	var out, errOut bytes.Buffer
	code := runServe([]string{"--bind", "0.0.0.0:7879"}, &out, &errOut)
	if code != serveExitFatal {
		t.Fatalf("exit = %d, want %d", code, serveExitFatal)
	}
	if !strings.Contains(errOut.String(), "loopback") {
		t.Errorf("stderr = %q, want a loopback explanation", errOut.String())
	}
}

func TestServeRejectsArguments(t *testing.T) {
	t.Parallel()
	if code := runServe([]string{"extra"}, &bytes.Buffer{}, &bytes.Buffer{}); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
}

func TestServeLockSingleInstance(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first, err := acquireServeLock(dir)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	if !first.Held() {
		t.Fatal("fresh lock is not held")
	}
	second, err := acquireServeLock(dir)
	if err == nil {
		if rerr := second.Release(); rerr != nil {
			t.Errorf("release second lock: %v", rerr)
		}
		t.Fatal("second lock succeeded, want errServeLocked")
	}
	if !errors.Is(err, errServeLocked) {
		t.Fatalf("second lock error = %v, want errServeLocked", err)
	}
	if !strings.Contains(err.Error(), "pid") {
		t.Errorf("refusal lacks the owner PID: %v", err)
	}
	if rerr := first.Release(); rerr != nil {
		t.Fatalf("release: %v", rerr)
	}
	if first.Held() {
		t.Error("released lock still reports held")
	}
	third, err := acquireServeLock(dir)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	if rerr := third.Release(); rerr != nil {
		t.Fatalf("release third: %v", rerr)
	}
}

func TestDrainWaitsForActiveTurn(t *testing.T) {
	t.Parallel()
	d := newServeDaemon()
	defer d.Close()
	_, end, ok := d.BeginTurn()
	if !ok {
		t.Fatal("BeginTurn refused before draining")
	}
	time.AfterFunc(40*time.Millisecond, end)
	start := time.Now()
	if !d.Drain(time.Second) {
		t.Fatal("Drain returned before the active turn finished")
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Fatalf("Drain returned after %s; it did not wait for the turn", elapsed)
	}
}

// TestDrainNegativeControl simulates a stop that does not wait: the shared
// turn context is cancelled immediately, so a turn that needs 40 ms never
// completes. This is the control that makes the "turn completed" assertion of
// the e2e drain test meaningful.
func TestDrainNegativeControl(t *testing.T) {
	t.Parallel()
	d := newServeDaemon()
	defer d.Close()
	ctx, end, ok := d.BeginTurn()
	if !ok {
		t.Fatal("BeginTurn refused before draining")
	}
	completed := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer end()
		timer := time.NewTimer(40 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			close(completed)
		case <-ctx.Done():
		}
	}()
	d.cancel() // no drain wait
	<-finished
	select {
	case <-completed:
		t.Fatal("negative control completed a turn without draining")
	default:
	}
}

func TestDrainCancelsAfterWindow(t *testing.T) {
	t.Parallel()
	d := newServeDaemon()
	defer d.Close()
	ctx, end, ok := d.BeginTurn()
	if !ok {
		t.Fatal("BeginTurn refused before draining")
	}
	stopped := make(chan struct{})
	go func() {
		defer end()
		<-ctx.Done()
		close(stopped)
	}()
	if d.Drain(10 * time.Millisecond) {
		t.Fatal("Drain reported a clean drain; the turn ignored the window")
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("turn context was not cancelled at the drain deadline")
	}
	if _, _, ok := d.BeginTurn(); ok {
		t.Fatal("BeginTurn accepted work while draining")
	}
}

// TestDbWritableProbe proves the readiness probe executes a real write
// transaction and reports a cancelled context (the /readyz 503 path).
func TestDbWritableProbe(t *testing.T) {
	t.Parallel()
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: t.TempDir(), Kind: "serve"})
	if err != nil {
		t.Fatalf("open ctxdb: %v", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close ctxdb: %v", cerr)
		}
	}()
	if err := db.ProbeWrite(context.Background()); err != nil {
		t.Fatalf("healthy probe: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := db.ProbeWrite(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("probe error = %v, want context.Canceled", err)
	}
}
