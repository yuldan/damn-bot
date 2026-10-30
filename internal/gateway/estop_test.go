package gateway_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/gateway"
)

func estopAt(t *testing.T, dir string) gateway.Estop {
	t.Helper()
	return gateway.Estop{Dir: dir}
}

// TestEstopStateFile: a missing file is normal and the first pause writes a
// private 0600 file.
func TestEstopStateFile(t *testing.T) {
	dir := t.TempDir()
	stop := estopAt(t, dir)
	st, err := stop.Status(t.Context())
	if err != nil || st.Paused {
		t.Fatalf("missing state = %+v, %v; want normal", st, err)
	}
	if _, perr := stop.Pause(t.Context(), "maintenance"); perr != nil {
		t.Fatal(perr)
	}
	fi, err := os.Stat(filepath.Join(dir, "estop.json"))
	if err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("state file = %v, %v; want 0600", fi, err)
	}
}

// TestEstopPauseResumeIdempotent: repeated pause/resume keep the original
// reason, since and parked window.
func TestEstopPauseResumeIdempotent(t *testing.T) {
	stop := estopAt(t, t.TempDir())
	first, err := stop.Pause(t.Context(), "maintenance")
	if err != nil || !first.Paused || first.Reason != "maintenance" || first.Since.IsZero() {
		t.Fatalf("pause = %+v, %v", first, err)
	}
	again, err := stop.Pause(t.Context(), "other")
	if err != nil || !again.Since.Equal(first.Since) || again.Reason != "maintenance" {
		t.Fatalf("second pause = %+v, %v; want unchanged", again, err)
	}
	off, err := stop.Resume(t.Context())
	if err != nil || off.Paused || !off.PendingSince.Equal(first.Since) {
		t.Fatalf("resume = %+v, %v", off, err)
	}
	if off, err = stop.Resume(t.Context()); err != nil || !off.PendingSince.Equal(first.Since) {
		t.Fatalf("second resume = %+v, %v; pending window must stay", off, err)
	}
}

// TestEstopDrainedClearsWindow: Drained clears only the replay watermark.
func TestEstopDrainedClearsWindow(t *testing.T) {
	dir := t.TempDir()
	stop := estopAt(t, dir)
	first, err := stop.Pause(t.Context(), "maintenance")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stop.Resume(t.Context()); err != nil {
		t.Fatal(err)
	}
	if derr := stop.Drained(t.Context()); derr != nil {
		t.Fatal(derr)
	}
	st, serr := stop.Status(t.Context())
	if serr != nil || st.Paused || !st.PendingSince.IsZero() {
		t.Fatalf("after drain = %+v, %v (pause since %s)", st, serr, first.Since)
	}
}

// TestEstopUnreadableFailsClosed: a corrupt file reports paused with an error,
// so every caller that only looks at Paused refuses new work.
func TestEstopUnreadableFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "estop.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := (gateway.Estop{Dir: dir}).Status(t.Context())
	if err == nil || !st.Paused || st.Reason == "" {
		t.Fatalf("corrupt state = %+v, %v; want paused+error", st, err)
	}
	// The owner can still recover explicitly; Resume rewrites a clean state.
	if off, rerr := (gateway.Estop{Dir: dir}).Resume(t.Context()); rerr != nil || off.Paused {
		t.Fatalf("resume over corrupt state = %+v, %v", off, rerr)
	}
}

// TestGateEmergencyStopParksTurn: while the stop is on, an ordinary turn is
// recorded for replay but never runs, and an unreadable state fails closed.
func TestGateEmergencyStopParksTurn(t *testing.T) {
	dir := t.TempDir()
	stop := estopAt(t, dir)
	if _, err := stop.Pause(t.Context(), "test"); err != nil {
		t.Fatal(err)
	}
	inbox := &fakeInbox{seen: map[string]bool{}}
	g, err := gateway.New(&gateway.Options{
		Identities: &fakeIdentities{rows: map[string]gateway.Identity{"telegram:1": owner("1")}},
		Inbox:      inbox, Estop: &stop,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := &runner{}
	out, err := g.Admit(t.Context(), inbound("c1", "1", "u:1"), run.run)
	if err != nil || out.Kind != gateway.Rejected || out.Reason != gateway.ReasonPaused {
		t.Fatalf("outcome = %+v, %v; want rejected/paused", out, err)
	}
	if run.calls != 0 || inbox.calls != 1 {
		t.Fatalf("runner=%d inbox=%d; want 0 run, 1 record", run.calls, inbox.calls)
	}
	// Replaying the same update is a duplicate, not a second parked row.
	out, err = g.Admit(t.Context(), inbound("c1", "1", "u:1"), run.run)
	if err != nil || out.Kind != gateway.Duplicate {
		t.Fatalf("replay = %+v, %v; want duplicate", out, err)
	}
}

func TestGateEmergencyStopAllowsPauseCommand(t *testing.T) {
	dir := t.TempDir()
	stop := estopAt(t, dir)
	if _, err := stop.Pause(t.Context(), "test"); err != nil {
		t.Fatal(err)
	}
	commands := gateway.NewCommands(&gateway.CommandOptions{Estop: &stop})
	g, err := gateway.New(&gateway.Options{
		Identities: &fakeIdentities{rows: map[string]gateway.Identity{"telegram:1": owner("1")}},
		Inbox:      &fakeInbox{seen: map[string]bool{}}, Commands: commands, Estop: &stop,
	})
	if err != nil {
		t.Fatal(err)
	}
	in := inbound("c1", "1", "u:1")
	in.Text = "/pause off"
	out, err := g.Admit(t.Context(), in, (&runner{}).run)
	if err != nil || out.Kind != gateway.CommandHandled || !strings.Contains(out.Reply, "off") {
		t.Fatalf("outcome = %+v, %v; want handled /pause off", out, err)
	}
	if st, serr := stop.Status(t.Context()); serr != nil || st.Paused {
		t.Fatalf("stop after /pause off = %+v, %v", st, serr)
	}
}

// TestGateEmergencyStopUnreadableFailsClosed proves an unreadable state is
// treated as paused and still parks the update.
func TestGateEmergencyStopUnreadableFailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "estop.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	inbox := &fakeInbox{seen: map[string]bool{}}
	g, err := gateway.New(&gateway.Options{
		Identities: &fakeIdentities{rows: map[string]gateway.Identity{"telegram:1": owner("1")}},
		Inbox:      inbox, Estop: &gateway.Estop{Dir: dir},
	})
	if err != nil {
		t.Fatal(err)
	}
	run := &runner{}
	out, err := g.Admit(t.Context(), inbound("c1", "1", "u:1"), run.run)
	if err == nil || out.Reason != gateway.ReasonPaused || run.calls != 0 || inbox.calls != 1 {
		t.Fatalf("outcome = %+v, %v; run=%d inbox=%d", out, err, run.calls, inbox.calls)
	}
}

// TestGateAdmitPendingRunsOnce: replay runs the stored update without writing
// the inbox again, and refuses to run while the stop came back on.
func TestGateAdmitPendingRunsOnce(t *testing.T) {
	dir := t.TempDir()
	stop := estopAt(t, dir)
	inbox := &fakeInbox{seen: map[string]bool{}}
	g, err := gateway.New(&gateway.Options{
		Identities: &fakeIdentities{rows: map[string]gateway.Identity{"telegram:1": owner("1")}},
		Inbox:      inbox, Estop: &stop,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := &runner{}
	out, err := g.AdmitPending(t.Context(), inbound("c1", "1", "u:1"), run.run)
	if err != nil || out.Kind != gateway.Accepted || run.calls != 1 || inbox.calls != 0 {
		t.Fatalf("pending = %+v, %v; run=%d inbox=%d", out, err, run.calls, inbox.calls)
	}
	if _, perr := stop.Pause(t.Context(), "again"); perr != nil {
		t.Fatal(perr)
	}
	out, err = g.AdmitPending(t.Context(), inbound("c1", "1", "u:2"), run.run)
	if err != nil || out.Reason != gateway.ReasonPaused || run.calls != 1 {
		t.Fatalf("paused pending = %+v, %v; run=%d", out, err, run.calls)
	}
}

// TestPauseCommandOwnerOnlyAndIdempotent: user is refused before the handler,
// owner toggles the stop, and /pause off twice is honest.
func TestPauseCommandOwnerOnlyAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	stop := estopAt(t, dir)
	c := gateway.NewCommands(&gateway.CommandOptions{Estop: &stop})
	if res := c.Dispatch(t.Context(), testTicket(gateway.RoleUser), "/pause"); !res.Handled || !strings.Contains(res.Reply, "owner-only") {
		t.Fatalf("/pause as user = %+v", res)
	}
	res := c.Dispatch(t.Context(), testTicket(gateway.RoleOwner), "/pause maintenance window")
	if !res.Handled || !strings.Contains(res.Reply, "emergency stop on") {
		t.Fatalf("/pause = %+v", res)
	}
	if res = c.Dispatch(t.Context(), testTicket(gateway.RoleOwner), "/pause again"); !strings.Contains(res.Reply, "already on") {
		t.Fatalf("second /pause = %+v", res)
	}
	if res = c.Dispatch(t.Context(), testTicket(gateway.RoleOwner), "/pause off"); !strings.Contains(res.Reply, "off") {
		t.Fatalf("/pause off = %+v", res)
	}
	if res = c.Dispatch(t.Context(), testTicket(gateway.RoleOwner), "/pause off"); !strings.Contains(res.Reply, "not on") {
		t.Fatalf("second /pause off = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "estop.json")); err != nil {
		t.Fatalf("state file missing: %v", err)
	}
}
