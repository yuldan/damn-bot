package exec

import (
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func startCmd(t *testing.T, script string) *osProc {
	t.Helper()
	p, err := exec.LookPath("cmd")
	if err != nil {
		t.Skip("no cmd.exe")
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "SystemRoot=" + os.Getenv("SystemRoot")}
	w, err := start(&Spec{Argv: []string{p, "/d", "/c", script}, Dir: t.TempDir(), Env: env}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.release(); err != nil {
			t.Error(err)
		}
	})
	return w
}

func TestProcJobExit(t *testing.T) {
	t.Parallel()
	w := startCmd(t, "echo out& exit 4")
	b, err := io.ReadAll(w.output())
	if err != nil {
		t.Fatal(err)
	}
	ex, err := w.wait()
	if err != nil || ex.code != 4 || ex.signal != "" || strings.TrimSpace(string(b)) != "out" {
		t.Fatalf("%+v %v %q", ex, err, b)
	}
}

func TestProcJobKillTree(t *testing.T) {
	t.Parallel()
	w := startCmd(t, "start /b ping -n 100 127.0.0.1 >NUL & echo up& ping -n 100 127.0.0.1 >NUL")
	buf := make([]byte, 2)
	if _, err := io.ReadFull(w.output(), buf); err != nil {
		t.Fatal(err)
	}
	if !w.treeAlive() {
		t.Fatal("job empty while running")
	}
	if err := w.terminate(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.wait(); err != nil {
		t.Fatal(err)
	}
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	deadline := time.After(5 * time.Second)
	for w.treeAlive() {
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatal("job members survived TerminateJobObject")
		}
	}
	if err := w.closeOutput(); err != nil {
		t.Fatal(err)
	}
}

// TestProcConPTY (§A.1): a ConPTY child sees a console; its output reaches
// EOF once the pseudo console is closed.
func TestProcConPTY(t *testing.T) {
	t.Parallel()
	p, err := exec.LookPath("cmd")
	if err != nil {
		t.Skip("no cmd.exe")
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "SystemRoot=" + os.Getenv("SystemRoot")}
	w, err := start(&Spec{Argv: []string{p, "/d", "/c", "echo tty& exit 5"}, Dir: t.TempDir(), Env: env, PTY: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := w.release(); err != nil {
			t.Error(err)
		}
	})
	ex, werr := w.wait()
	if werr != nil || ex.code != 5 {
		t.Fatalf("%+v %v", ex, werr)
	}
	// The few bytes fit the pipe buffer, so closing before reading cannot
	// block the console.
	if cerr := w.closeConsole(); cerr != nil {
		t.Fatal(cerr)
	}
	out, _ := io.ReadAll(w.output()) //nolint:errcheck // EOF or a broken pipe ends the read
	if !strings.Contains(string(out), "tty") {
		t.Fatalf("%q", out)
	}
}
