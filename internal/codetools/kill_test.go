package codetools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/sandbox"
)

// Child-process protocol of the CT-25 crash tests: the parent starts the
// child in the test directory and sets these variables, the child writes
// forever and prints "w" after each write and "at" when it parks at the
// kill point.
const (
	kill9Env      = "DAMNBOT_CT25_CHILD" // "1" in the child
	kill9PointEnv = "DAMNBOT_CT25_POINT" // commit point to park at ("" = never)
	kill9SkipEnv  = "DAMNBOT_CT25_SKIP"  // writes completed before parking
	kill9LinkEnv  = "DAMNBOT_CT25_LINK"  // "1": f.txt is hard-linked (approved, in place)
)

// kill9Versions are the only contents the file may ever hold.
func kill9Versions() [2]string {
	return [2]string{strings.Repeat("old version\n", 20000), strings.Repeat("NEW VERSION, longer\n", 30000)}
}

// TestAtomicWriteKill9 is CT-25: a child process rewrites a file in a loop
// through write_file and is killed (SIGKILL / TerminateProcess) 200 times:
// parked inside the commit at each kill point (mid-temp: the temp is half
// written; pre-rename: the temp is complete; post-rename: the directory is
// not yet fsynced) and at random times. The file always holds a whole
// version, never a mix, and CleanOrphanTemps removes every crash temp.
// A commit that wrote the target in place would never reach pre-rename
// or post-rename, which fails the test.
func TestAtomicWriteKill9(t *testing.T) {
	if os.Getenv(kill9Env) != "" {
		kill9Child(t)
		return
	}
	t.Parallel()
	dir := realTemp(t)
	vers := kill9Versions()
	p := filepath.Join(dir, "f.txt")
	writeFile(t, p, vers[0])
	rng := testRand(25)
	points := []string{"mid-temp", "pre-rename", "post-rename", ""}
	for i := range 200 {
		kill9Run(t, dir, points[i%len(points)], rng.IntN(4), false)
		if got := string(readFixture(t, p)); got != vers[0] && got != vers[1] {
			t.Fatalf("iteration %d (%q): partial file (%d bytes)", i, points[i%len(points)], len(got))
		}
	}
	e := kill9Engine(t, dir)
	if _, err := e.CleanOrphanTemps(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ents, err := os.ReadDir(dir); err != nil || len(ents) != 1 {
		t.Errorf("orphan temps left: %d entries (%v)", len(ents), err)
	}
}

// kill9Run starts a child that parks at point after skip writes (point
// "": killed after skip writes) and kills it there.
func kill9Run(t *testing.T, dir, point string, skip int, link bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestAtomicWriteKill9$") //nolint:gosec // G204: re-exec of the test binary.
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), kill9Env+"=1", kill9PointEnv+"="+point, kill9SkipEnv+"="+strconv.Itoa(skip), kill9LinkEnv+"="+map[bool]string{true: "1"}[link])
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	// The child parks by reading stdin, which stays open (never written)
	// until Wait closes it after the kill.
	if _, err = cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	awaitKillPoint(t, out, point, skip)
	if err = cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatal(err)
	}
	if werr := cmd.Wait(); werr == nil {
		t.Fatal("child exited by itself")
	}
}

// awaitKillPoint reads the child's progress until it parks at point (or,
// for point "", completes skip writes).
func awaitKillPoint(t *testing.T, out io.Reader, point string, skip int) {
	t.Helper()
	sc := bufio.NewScanner(out)
	writes := 0
	for sc.Scan() {
		switch sc.Text() {
		case "at":
			return
		case "w":
			if writes++; point == "" && writes > skip {
				return
			} else if writes > skip+3 {
				t.Fatalf("kill point %q not reached in %d writes", point, writes)
			}
		}
	}
	t.Fatalf("child ended before the kill point %q", point)
}

func kill9Engine(t *testing.T, dir string) *Engine {
	t.Helper()
	sb, err := sandbox.New(&sandbox.Config{Roots: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(&Config{Sandbox: sb, Roots: []string{dir}, Home: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}

// kill9Child alternates the two versions forever, printing after each
// write; it parks at the chosen commit point until killed.
func kill9Child(t *testing.T) {
	point := os.Getenv(kill9PointEnv)
	skip, err := strconv.Atoi(os.Getenv(kill9SkipEnv))
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	e := kill9Engine(t, dir)
	o := Opts{Approved: os.Getenv(kill9LinkEnv) == "1"}
	i := 0
	e.hook = func(p string) error {
		if p == point && i >= skip {
			fmt.Println("at") //nolint:forbidigo // progress protocol to the parent process.
			// Park until killed: the parent holds stdin open and never writes.
			if _, perr := io.Copy(io.Discard, os.Stdin); perr != nil {
				return fmt.Errorf("ct25 park: %w", perr)
			}
			return errors.New("ct25 park: stdin closed before the kill")
		}
		return nil
	}
	ctx := context.Background()
	vers := kill9Versions()
	for ; ; i++ {
		r, err := e.Read(ctx, ReadArgs{Path: "f.txt", Limit: 1}, o)
		if err != nil {
			t.Fatal(err)
		}
		tag := metaStr(&r, "tag")
		if _, err := e.Write(ctx, WriteArgs{Path: "f.txt", Content: vers[(i+1)%2], Tag: &tag}, o); err != nil {
			t.Fatal(err)
		}
		fmt.Println("w") //nolint:forbidigo // progress protocol to the parent process.
	}
}
