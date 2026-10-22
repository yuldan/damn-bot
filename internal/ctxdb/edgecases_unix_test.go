//go:build unix

package ctxdb

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// platformStateCases are EC-STATE rows whose mechanism exists on POSIX only.
func platformStateCases(g phase.Gate) []stateCase {
	return []stateCase{{"EC-STATE-016", func(t *testing.T) { ecState016(t, g) }}}
}

// 016: a child that inherits the lock descriptor keeps the flock alive after
// the parent closes its own copy. The lock must be reported as held
// (fail closed) until the child exits, then become available again.
func ecState016(t *testing.T, g phase.Gate) {
	if !g.Item(t, "EC-STATE-016", "F2", "covered") {
		return
	}
	r := testRoot(t, t.TempDir())
	f, err := tryLock(r, "inherited.lock", true)
	must(t, err)
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestChildProcess$", "-test.count=1") //nolint:gosec // G204: re-exec of this test binary
	cmd.Env = append(os.Environ(), childRoleEnv+"=hold-fd")
	cmd.ExtraFiles = []*os.File{f}
	stdin, err := cmd.StdinPipe()
	must(t, err)
	stdout, err := cmd.StdoutPipe()
	must(t, err)
	must(t, cmd.Start())
	sc := bufio.NewScanner(stdout)
	if !sc.Scan() || sc.Text() != "READY" {
		t.Fatalf("child did not become ready: %q", sc.Text())
	}
	must(t, f.Close()) // only the child's inherited descriptor remains
	if _, lerr := tryLock(r, "inherited.lock", true); !errors.Is(lerr, ErrDBBusy) {
		t.Fatalf("inherited flock not honoured: %v", lerr)
	}
	must(t, stdin.Close())
	must(t, cmd.Wait())
	if again, lerr := tryLock(r, "inherited.lock", true); lerr != nil {
		t.Fatalf("lock not released after child exit: %v", lerr)
	} else {
		must(t, unlockFile(again))
	}
}
