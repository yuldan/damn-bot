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

func TestProcPTYUnavailableWindows(t *testing.T) {
	t.Parallel()
	if _, err := start(&Spec{Argv: []string{"cmd"}, PTY: true}, nil); err == nil {
		t.Fatal("pty accepted")
	}
}
