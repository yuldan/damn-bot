//go:build linux || darwin

package ctxdb

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"syscall"
	"testing"
	"time"
)

func procScenarios() []scenario {
	return []scenario{
		{"S062_CtrlZDuringExecToolGroup", s062},
		{"S074_OrphanStoppedToolGroupKilled", s074},
		{"S085_LeaderDeadNohupChildKilled", s085},
	}
}

// spawnGroup starts `sh -c script` as leader of a new process group.
func spawnGroup(t *testing.T, script string) (*exec.Cmd, int, string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "sh", "-c", script) //nolint:gosec // G204: scripts are test literals
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	must(t, cmd.Start())
	t.Cleanup(func() {
		t.Log(syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)) // ESRCH when the group is already gone
		t.Log(cmd.Wait())
	})
	st := startStamp(t, cmd.Process.Pid)
	return cmd, cmd.Process.Pid, st
}

// procGroupMembers lists live members of a process group (test helper).
func procGroupMembers(pgid int) []int {
	m := pgroupMembers(pgid)
	out := make([]int, 0, len(m))
	for pid := range m {
		out = append(out, pid)
	}
	slices.Sort(out)
	return out
}

// waitGroupGone waits (bounded, ticker — no sleep) until no live member remains.
func waitGroupGone(t *testing.T, pgid int) {
	t.Helper()
	tk := time.NewTicker(20 * time.Millisecond)
	defer tk.Stop()
	deadline := time.After(10 * time.Second)
	for len(procGroupMembers(pgid)) > 0 {
		select {
		case <-tk.C:
		case <-deadline:
			t.Fatalf("process group %d still has members %v", pgid, procGroupMembers(pgid))
		}
	}
}

// waitMembers waits until the group has at least n live members.
func waitMembers(t *testing.T, pgid, n int) {
	t.Helper()
	tk := time.NewTicker(10 * time.Millisecond)
	defer tk.Stop()
	deadline := time.After(10 * time.Second)
	for len(procGroupMembers(pgid)) < n {
		select {
		case <-tk.C:
		case <-deadline:
			t.Fatalf("group %d has %d members", pgid, len(procGroupMembers(pgid)))
		}
	}
}

// resumeToolGroup is the SIGCONT reference path (§11.11.7 SIGTSTP): the
// group continues only if this process still owns the lease, else SIGKILL.
func resumeToolGroup(s *Store, sid string, pgid int) error {
	if err := s.Write(context.Background(), func(*Tx) error { return nil }); err != nil { // reopen
		return err
	}
	if err := s.CheckLease(context.Background(), sid); err != nil {
		return errors.Join(err, syscall.Kill(-pgid, syscall.SIGKILL))
	}
	return wrap(syscall.Kill(-pgid, syscall.SIGCONT))
}

// #62: Ctrl-Z during exec: the group is stopped; on SIGCONT it continues
// only if the lease is still ours, otherwise it is killed.
func s062(t *testing.T) {
	e := newEnv(t)
	tui, srv := e.open(1, "tui"), e.open(2, "serve")
	for _, sid := range []string{"A", "S"} {
		session(t, tui, sid, "")
	}
	_, pgA, _ := spawnGroup(t, "sleep 300")
	_, pgS, _ := spawnGroup(t, "sleep 300")
	must(t, tui.Suspend())
	for _, pg := range []int{pgA, pgS} {
		must(t, syscall.Kill(-pg, syscall.SIGSTOP))
	}
	e.clk.Advance(15 * time.Second)
	takeOverB(t, e, srv) // serve takes over S; A keeps its lease
	must(t, tui.Heartbeat(context.Background(), "A"))
	must(t, resumeToolGroup(tui, "A", pgA))
	wantErr(t, resumeToolGroup(tui, "S", pgS), ErrLeaseFenced)
	waitGroupGone(t, pgS)
	if st := procState(pgA); st == "T" || st == "" {
		t.Fatalf("group A state %q, want running", st)
	}
}

// #74: TUI SIGKILLed while its tool group is SIGSTOPped (orphaned group
// with a nohup child): the taker kills the group before recovery.
func s074(t *testing.T) {
	e := newEnv(t)
	tui := e.open(1, "tui")
	session(t, tui, "S", "")
	_, pg, st := spawnGroup(t, "nohup sleep 300 >/dev/null 2>&1 & sleep 300")
	waitMembers(t, pg, 2)
	must(t, syscall.Kill(-pg, syscall.SIGSTOP))
	must(t, tui.Write(context.Background(), func(tx *Tx) error {
		if err := tx.StartTurn("S", "T1"); err != nil {
			return err
		}
		return tx.ToolPending("S", "T1", &ToolCall{CallID: "c", Name: "shell", Class: "non_idempotent", ArgsSha: "a", Pgid: pg, PgidStart: st})
	}))
	e.procs.kill(1)
	srv := e.open(2, "serve") // startup recovery, case (a)
	waitGroupGone(t, pg)
	if count(t, srv, `SELECT count(*) FROM events WHERE kind = 'tool_result' AND json_extract(payload, '$.status') = 'outcome_unknown'`) != 1 {
		t.Fatal("recovery did not report outcome_unknown")
	}
}

// #85: without cgroup the leader died (SIGHUP) while a nohup child lives:
// membership scan (AF-07), not "leader alive", decides the kill.
func s085(t *testing.T) {
	e := newEnv(t)
	tui := e.open(1, "tui")
	session(t, tui, "S", "")
	leader, pg, st := spawnGroup(t, "nohup sleep 300 >/dev/null 2>&1 & echo started")
	t.Log(leader.Wait()) // leader exited; its child keeps pgrp = pg
	waitMembers(t, pg, 1)
	must(t, tui.Write(context.Background(), func(tx *Tx) error {
		if err := tx.StartTurn("S", "T1"); err != nil {
			return err
		}
		return tx.ToolPending("S", "T1", &ToolCall{CallID: "c", Name: "shell", Class: "non_idempotent", ArgsSha: "a", Pgid: pg, PgidStart: st})
	}))
	e.procs.kill(1)
	e.open(2, "serve")
	waitGroupGone(t, pg)
}
