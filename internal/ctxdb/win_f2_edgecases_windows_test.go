//go:build windows

package ctxdb

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestWindowsF2EdgeCases covers the EC-WIN entries the roadmap requires at F2
// (EC-WIN-002, EC-WIN-005, EC-WIN-011). They run on the windows-latest CI
// runner because every one of them is about a mechanism the POSIX suite
// cannot exercise: native PID liveness, LockFileEx instead of flock, and a
// stale maintenance marker under Windows file semantics.
func TestWindowsF2EdgeCases(t *testing.T) {
	// Not parallel: EC-WIN-005 calls t.Setenv to put a decoy fcntl on PATH,
	// and testing panics when a test that sets the environment (or any of its
	// parents) calls t.Parallel.
	g := phase.Current(t)
	cases := []stateCase{
		{"EC-WIN-002", ecWin002},
		{"EC-WIN-005", ecWin005},
		{"EC-WIN-011", ecWin011},
	}
	for _, c := range cases {
		if g.Item(t, c.id, "F2", "covered") {
			t.Run(c.id, c.fn)
		}
	}
}

// openNative opens dir with the real Windows clock and process probe (the
// env helpers inject fakes, which cannot show the native PID path).
func openNative(t *testing.T, dir, kind string, ident *Ident) *Store {
	t.Helper()
	o := Options{Dir: dir, Kind: kind, Ident: ident, BarrierWait: time.Second, BusyTimeout: 2 * time.Second}
	s, err := Open(context.Background(), &o)
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	t.Cleanup(func() { must(t, s.Close()) })
	return s
}

// deadPid returns the pid of a process that has already exited. Windows may
// recycle pids, but not between this Wait and the probe below.
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("cmd", "/c", "exit", "0") //nolint:gosec // G204: fixed argv
	if err := cmd.Run(); err != nil {
		t.Fatalf("dead-pid helper: %v", err)
	}
	return cmd.Process.Pid
}

// EC-WIN-002: resuming a session on native Windows must separate a stale PID
// marker from a live owner. The lease is written by a dead PID (the marker a
// crashed process leaves); a second Store on the same host takes the lease
// over and continues the transcript. A lease owned by a live PID stays busy.
func ecWin002(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	a := openNative(t, dir, "tui", nil)
	if _, err := a.CreateSession(ctx, Session{ID: "S", Cwd: `C:\w`}); err != nil {
		t.Fatal(err)
	}
	appendMsg(t, a, "S", "before resume")
	if _, err := a.CreateSession(ctx, Session{ID: "L", Cwd: `C:\w`}); err != nil {
		t.Fatal(err)
	}

	// The "process" that will resume the session has a different identity
	// (same host, other start stamp): only the lease owner's liveness can
	// decide whether the session is free.
	b := openNative(t, dir, "run", &Ident{Host: a.id.Host, PID: os.Getpid(), Start: "resumed", BootID: a.id.BootID})

	now := SystemClock{}.BootMs()
	dead := deadPid(t)
	if !(SystemProcs{}).Gone(Ident{Host: a.id.Host, PID: dead}) {
		t.Fatalf("native probe still reports the exited pid %d as present", dead)
	}
	setLease := func(sid string, pid int, start string) {
		t.Helper()
		must(t, a.Write(ctx, func(tx *Tx) error {
			_, err := tx.Exec(`UPDATE session_leases SET owner_pid=?, owner_start=?, owner_host=?,
				owner_boot_id=?, heartbeat_at=?, expires_at=? WHERE session_id=?`,
				pid, start, a.id.Host, a.id.BootID, now, now+LeaseTTL, sid)
			return err
		}))
	}

	// A live owner is not stale: the resume fails closed.
	start, err := procStart(os.Getpid())
	must(t, err)
	setLease("L", os.Getpid(), start)
	if _, err := b.AcquireLease(ctx, "L"); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("lease of a live pid: %v, want ErrSessionBusy", err)
	}

	// A dead owner is stale: the resume takes over and the transcript grows.
	setLease("S", dead, "")
	if _, err := b.AcquireLease(ctx, "S"); err != nil {
		t.Fatalf("resume with a stale pid marker: %v", err)
	}
	appendMsg(t, b, "S", "after resume")
	rows, err := b.History(ctx, "S", 0)
	must(t, err)
	if len(rows) != 2 || rows[0].Content != "before resume" || rows[1].Content != "after resume" {
		t.Fatalf("resumed history = %+v", rows)
	}
}

// EC-WIN-005: the lock is gated by the OS (build tag), never by whether an
// "fcntl" module happens to exist. A third-party fcntl on PATH or in the data
// dir must not turn the Windows LockFileEx path into a no-op.
func ecWin005(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fcntl.py"), []byte("# third-party fcntl\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+";"+os.Getenv("PATH"))
	if !fairLocks {
		t.Fatal("LockFileEx exists on Windows, so the fairness probe must be on")
	}
	r := testRoot(t, dir)
	if err := r.Mkdir("fcntl", 0o700); err != nil {
		t.Fatal(err)
	}
	ex, err := tryLock(r, "gate.lock", true)
	must(t, err)
	if _, err := tryLock(r, "gate.lock", false); !errors.Is(err, ErrDBBusy) {
		t.Fatalf("a fake fcntl disabled the OS lock: %v", err)
	}
	must(t, unlockFile(ex))
	sh, err := tryLock(r, "gate.lock", false)
	must(t, err)
	if _, err := tryLock(r, "gate.lock", true); !errors.Is(err, ErrDBBusy) {
		t.Fatalf("exclusive lock over a shared holder: %v", err)
	}
	must(t, unlockFile(sh))
}

// EC-WIN-011: a stale lock left by a killed process is cleared only when no
// live owner holds it. ctxdb's maintenance marker is the repo's equivalent of
// git's index.lock: a marker without a held owner lock belongs to a dead
// owner, while a held owner lock means an active process and the marker stays.
func ecWin011(t *testing.T) {
	dir := t.TempDir()
	must(t, openNative(t, dir, "tui", nil).Close())
	marker := filepath.Join(dir, "run", "maint.request")
	writeMarker := func() {
		t.Helper()
		if err := os.WriteFile(marker, []byte(`{"pid":999999,"reason":"restore","expected_s":1}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gone := func() bool {
		_, err := os.Stat(marker)
		return errors.Is(err, fs.ErrNotExist)
	}

	// No owner: the kernel released maint.owner when the owner died.
	writeMarker()
	if !clearStaleMarkerRoot(t, dir) && !gone() {
		t.Fatal("stale marker survived")
	}
	writeMarker()
	openNative(t, dir, "tui", nil) // open clears it through sharedLock
	if !gone() {
		t.Fatal("stale marker survived a Store open")
	}

	// A live owner (its owner lock is held) keeps the marker: the checkout is
	// busy and another process must not steal its lock.
	writeMarker()
	root, err := os.OpenRoot(dir)
	must(t, err)
	defer func() { must(t, root.Close()) }()
	owner, err := tryLock(root, ownerFile, true)
	must(t, err)
	defer func() { must(t, unlockFile(owner)) }()
	o := Options{Dir: dir, Kind: "tui", BarrierWait: 100 * time.Millisecond, BusyTimeout: time.Second}
	if s, err := Open(context.Background(), &o); !errors.Is(err, ErrDBBusy) {
		if s != nil {
			_ = s.Close()
		}
		t.Fatalf("open under a live owner: %v, want ErrDBBusy", err)
	}
	if gone() {
		t.Fatal("marker of a live owner was removed")
	}
}

// clearStaleMarkerRoot exercises the shared-lock guard of clearStaleMarker.
func clearStaleMarkerRoot(t *testing.T, dir string) bool {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { must(t, root.Close()) }()
	return clearStaleMarker(root)
}
