package codetools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/sandbox"
)

const kill9Env = "DAMNBOT_CT25_CHILD"

// kill9Versions are the only contents the file may ever hold.
func kill9Versions() [2]string {
	return [2]string{strings.Repeat("old version\n", 20000), strings.Repeat("NEW VERSION, longer\n", 30000)}
}

// TestAtomicWriteKill9 is CT-25: a child process rewrites a file in a loop
// through write_file; it is killed (SIGKILL / TerminateProcess) at random
// points 200 times. The file always holds a whole version, never a mix,
// and crash temps are found by CleanOrphanTemps.
//
//nolint:gocognit,gocyclo // spawn/kill/verify loop of the crash test
func TestAtomicWriteKill9(t *testing.T) {
	if dir := os.Getenv(kill9Env); dir != "" {
		kill9Child(t, dir)
		return
	}
	t.Parallel()
	dir := realTemp(t)
	vers := kill9Versions()
	p := filepath.Join(dir, "f.txt")
	writeFile(t, p, vers[0])
	rng := testRand(25)
	for i := range 200 {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestAtomicWriteKill9$") //nolint:gosec // G204: re-exec of the test binary.
		cmd.Env = append(os.Environ(), kill9Env+"="+dir)
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(out)
		for n := rng.IntN(4); n >= 0 && sc.Scan(); n-- { // kill after a random number of writes
		}
		if err = cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Fatal(err)
		}
		if werr := cmd.Wait(); werr == nil {
			t.Fatalf("iteration %d: child exited by itself", i)
		}
		got := readFixture(t, p)
		if string(got) != vers[0] && string(got) != vers[1] {
			t.Fatalf("iteration %d: partial file (%d bytes)", i, len(got))
		}
	}
	v := &env{root: dir}
	sb, err := sandbox.New(&sandbox.Config{Roots: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(&Config{Sandbox: sb, Roots: []string{dir}, Home: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err = e.CleanOrphanTemps(context.Background()); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(v.root)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 {
		t.Errorf("orphan temps left: %d entries", len(ents))
	}
}

// kill9Child alternates the two versions forever, printing after each.
func kill9Child(t *testing.T, dir string) {
	sb, err := sandbox.New(&sandbox.Config{Roots: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(&Config{Sandbox: sb, Roots: []string{dir}, Home: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ctx := context.Background()
	vers := kill9Versions()
	for i := 0; ; i++ {
		r, err := e.Read(ctx, ReadArgs{Path: "f.txt", Limit: 1}, Opts{})
		if err != nil {
			t.Fatal(err)
		}
		tag := metaStr(&r, "tag")
		if _, err := e.Write(ctx, WriteArgs{Path: "f.txt", Content: vers[(i+1)%2], Tag: &tag}, Opts{}); err != nil {
			t.Fatal(err)
		}
		fmt.Println("w", i) //nolint:forbidigo // progress protocol to the parent process.
	}
}
