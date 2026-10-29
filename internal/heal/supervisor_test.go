package heal

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSupervisorTransitions(t *testing.T) {
	clk := newTestClock()
	s := NewSupervisor(clk.Now)
	if got := s.Snapshot(); got.State != Starting {
		t.Fatalf("initial state = %s, want starting", got.State)
	}
	// Explicit edges only: degraded may not be reached from starting.
	mustIs(t, s.Degrade("disk full"), ErrTransition, "starting → degraded")
	clk.Advance(time.Second)
	mustOK(t, s.Start(), "start")
	if got := s.Snapshot(); got.State != Healthy || !got.Since.Equal(clk.Now()) {
		t.Fatalf("after start = %+v, want healthy at %v", got, clk.Now())
	}
	mustIs(t, s.Start(), ErrTransition, "healthy → healthy")
	mustOK(t, s.Degrade("disk full: 412 MiB free"), "degrade")
	if got := s.Snapshot(); got.State != Degraded || !strings.Contains(got.Reason, "412") {
		t.Fatalf("after degrade = %+v", got)
	}
	mustOK(t, s.Recover(), "recover")
	mustIs(t, s.Recover(), ErrTransition, "healthy → healthy via recover")
	mustOK(t, s.EnterSafeMode("5 crashes in 10 minutes"), "safe mode")
	if got := s.Snapshot(); got.State != SafeMode || got.Reason != "5 crashes in 10 minutes" {
		t.Fatalf("after safe mode = %+v", got)
	}
	mustIs(t, s.EnterSafeMode("again"), ErrTransition, "safe_mode → safe_mode")
	mustOK(t, s.Resume(), "resume")
	mustIs(t, s.Resume(), ErrTransition, "healthy → healthy via resume")
	mustOK(t, s.Exit(70), "exit")
	if got := s.Snapshot(); got.State != Exited || got.ExitCode != 70 {
		t.Fatalf("after exit = %+v, want exited(70)", got)
	}
	mustIs(t, s.Start(), ErrTransition, "exited → healthy")
	mustIs(t, s.Exit(0), ErrTransition, "exited → exited")
	// A fatal start still exits from starting.
	mustOK(t, NewSupervisor(nil).Exit(78), "exit from starting")
}

func TestPermitStartingAndHealthy(t *testing.T) {
	s := NewSupervisor(nil)
	mustIs(t, s.Permit(ToolExec), ErrNotReady, "tool in starting")
	mustOK(t, s.Start(), "start")
	// Negative control: these actions are allowed outside safe mode.
	for _, a := range []Action{ModuleStart, AutoTurn, ToolExec, OwnerCommand} {
		mustOK(t, s.Permit(a), "healthy "+string(a))
	}
}

func TestPermitSafeMode(t *testing.T) {
	s := NewSupervisor(nil)
	mustOK(t, s.Start(), "start")
	reason := "5 crashes in 10 minutes"
	mustOK(t, s.EnterSafeMode(reason), "safe mode")
	for _, a := range []Action{ModuleStart, AutoTurn, ToolExec} {
		err := s.Permit(a)
		mustIs(t, err, ErrSafeMode, "safe mode "+string(a))
		var d *Denial
		if !errors.As(err, &d) || d.Action != a || d.State != SafeMode || !strings.Contains(d.Reason, reason) {
			t.Fatalf("safe mode %s denial = %#v", a, err)
		}
	}
	mustOK(t, s.Permit(OwnerCommand), "owner command in safe mode")
	mustOK(t, s.Resume(), "resume")
	mustOK(t, s.Degrade("disk full"), "degrade")
	mustIs(t, s.Permit(AutoTurn), ErrDegraded, "degraded auto turn")
	mustOK(t, s.Permit(ToolExec), "degraded tool")
	mustOK(t, s.Exit(70), "exit")
	mustIs(t, s.Permit(OwnerCommand), ErrExited, "exited permit")
}
