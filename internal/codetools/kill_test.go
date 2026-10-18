package codetools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/sandbox"
)

// Child-process protocol of the CT-25 crash tests: the parent sets these
// variables, the child writes forever and prints "w" after each write and
// "at" when it parks at the kill point.
const (
	kill9Env      = "DAMNBOT_CT25_CHILD"
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

// TestInPlaceKill9 is CT-25 for a hard-linked file, rewritten in place
// (approved, CT27-8): killed while the temp is half written the file is
// whole; killed mid-copy the file may be partial, but the fsynced backup
// next to it holds the whole previous version, and CleanOrphanTemps keeps
// it.
func TestInPlaceKill9(t *testing.T) {
	if os.Getenv(kill9Env) != "" {
		t.Skip("child runs under TestAtomicWriteKill9")
	}
	t.Parallel()
	dir, out := realTemp(t), realTemp(t)
	vers := kill9Versions()
	p := filepath.Join(dir, "f.txt")
	writeFile(t, p, vers[0])
	if err := os.Link(p, filepath.Join(out, "other.txt")); err != nil {
		t.Skipf("Link: %v", err)
	}
	e := kill9Engine(t, dir)
	for i := range 20 {
		point, skip := []string{"mid-temp", "mid-copy"}[i%2], i%3
		kill9Run(t, dir, point, skip, true)
		if _, err := e.CleanOrphanTemps(context.Background()); err != nil {
			t.Fatal(err)
		}
		got := string(readFixture(t, p))
		baks, _ := filepath.Glob(filepath.Join(dir, ".f.txt.damnbot-bak-*"))
		// write n stores vers[(n+1)%2], so the parked write found vers[skip%2].
		switch {
		case point == "mid-temp" && (got != vers[skip%2] || len(baks) != 0):
			t.Fatalf("iteration %d: file changed before the copy (%d bytes, backups %v)", i, len(got), baks)
		case point == "mid-copy" && (len(baks) != 1 || string(readFixture(t, baks[0])) != vers[skip%2]):
			t.Fatalf("iteration %d: no whole backup of the previous version: %v", i, baks)
		}
		writeFile(t, p, vers[0]) // recover, as from file_snapshots
		for _, b := range baks {
			if err := os.Remove(b); err != nil {
				t.Fatal(err)
			}
		}
	}
	if ents, err := os.ReadDir(dir); err != nil || len(ents) != 1 {
		t.Errorf("crash temps left: %d entries (%v)", len(ents), err)
	}
}

// kill9Run starts a child that parks at point after skip writes (point
// "": killed after skip writes) and kills it there.
func kill9Run(t *testing.T, dir, point string, skip int, link bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAtomicWriteKill9$") //nolint:gosec // G204: re-exec of the test binary.
	cmd.Env = append(os.Environ(), kill9Env+"="+dir, kill9PointEnv+"="+point, kill9SkipEnv+"="+strconv.Itoa(skip), kill9LinkEnv+"="+map[bool]string{true: "1"}[link])
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(out)
	writes, parked := 0, false
	for !parked && sc.Scan() {
		switch sc.Text() {
		case "at":
			parked = true
		case "w":
			if writes++; point == "" && writes > skip {
				parked = true
			} else if writes > skip+3 {
				t.Fatalf("kill point %q not reached in %d writes", point, writes)
			}
		}
	}
	if !parked {
		t.Fatalf("child ended before the kill point %q", point)
	}
	if err = cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatal(err)
	}
	if werr := cmd.Wait(); werr == nil {
		t.Fatal("child exited by itself")
	}
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
// write; it sleeps at the chosen commit point until killed.
func kill9Child(t *testing.T) {
	dir, point := os.Getenv(kill9Env), os.Getenv(kill9PointEnv)
	skip, _ := strconv.Atoi(os.Getenv(kill9SkipEnv))
	e := kill9Engine(t, dir)
	o := Opts{Approved: os.Getenv(kill9LinkEnv) == "1"}
	i := 0
	e.hook = func(p string) error {
		if p == point && i >= skip {
			fmt.Println("at") //nolint:forbidigo // progress protocol to the parent process.
			time.Sleep(time.Hour)
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
