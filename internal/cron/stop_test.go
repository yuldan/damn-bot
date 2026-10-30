package cron

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestEmergencyStopLeavesCatchUp: while the stop is on a due slot is not
// claimed or queued; after resume the existing catch-up policy runs it once.
func TestEmergencyStopLeavesCatchUp(t *testing.T) {
	store := newMemStore(testJob())
	exec := &countedExecutor{}
	clock := &fakeClock{now: at(10, 15)}
	stopped := true
	s := &Scheduler{
		Store: store, Executor: exec, Clock: clock,
		Stopped: func(context.Context) (bool, error) { return stopped, nil },
	}
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if exec.count() != 0 || len(store.rows) != 0 {
		t.Fatalf("stopped tick ran=%d rows=%d, want 0/0", exec.count(), len(store.rows))
	}
	stopped = false
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	if exec.count() != 1 {
		t.Fatalf("resumed tick ran %d slots, want 1", exec.count())
	}
}

// TestEmergencyStopReadErrorFailsClosed: an unreadable stop state starts no
// job and surfaces the error instead of silently running work.
func TestEmergencyStopReadErrorFailsClosed(t *testing.T) {
	store := newMemStore(testJob())
	exec := &countedExecutor{}
	s := &Scheduler{
		Store: store, Executor: exec, Clock: &fakeClock{now: at(10, 15)},
		Stopped: func(context.Context) (bool, error) {
			return true, errors.New("state unreadable")
		},
	}
	err := s.Tick(t.Context())
	if err == nil || !strings.Contains(err.Error(), "emergency stop") {
		t.Fatalf("err = %v, want emergency stop error", err)
	}
	if exec.count() != 0 || len(store.rows) != 0 {
		t.Fatalf("run=%d rows=%d, want 0/0", exec.count(), len(store.rows))
	}
}
