//go:build unix

package exec

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"
)

// TestRunStdinUnreadBrokenPipe: a child that exits without reading a large
// stdin closes the pipe; the runner's stdin writer must treat EPIPE as
// expected, and Run must still report the child's exit status.
func TestRunStdinUnreadBrokenPipe(t *testing.T) {
	t.Parallel()
	res := run(t.Context(), t, newTestRunner(t), &Spec{
		Argv:    []string{"/bin/sh", "-c", "exit 0"},
		Stdin:   bytes.Repeat([]byte("x"), 1<<20),
		Timeout: 10 * time.Second,
	})
	if res.Status != StatusExited || res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("status %s, code %v", res.Status, res.ExitCode)
	}
}

// TestRunOnOutputMatchesOutput: the chunks streamed to OnOutput are exactly
// the captured bytes, in order, so a consumer can concatenate them as the
// live view of the same output Result.Output keeps.
func TestRunOnOutputMatchesOutput(t *testing.T) {
	t.Parallel()
	var chunks [][]byte
	res := run(t.Context(), t, newTestRunner(t), &Spec{
		Argv:     []string{"/bin/sh", "-c", "printf 'one\\ntwo\\nthree\\n'"},
		OnOutput: func(b []byte) { chunks = append(chunks, bytes.Clone(b)) },
	})
	if len(chunks) == 0 {
		t.Fatal("OnOutput was never called")
	}
	if got := bytes.Join(chunks, nil); !bytes.Equal(got, res.Output) {
		t.Fatalf("streamed %q, Output %q", got, res.Output)
	}
}

// TestRunPTYSpawnFailed: a missing binary fails before any tree exists and
// the result still reports the requested runner kind.
func TestRunPTYSpawnFailed(t *testing.T) {
	t.Parallel()
	spec := &Spec{Argv: []string{filepath.Join(t.TempDir(), "missing")}, PTY: true}
	res, err := newTestRunner(t).Run(t.Context(), spec)
	if err == nil || res.Status != StatusSpawnFailed || res.Runner != RunnerPTY {
		t.Fatalf("err %v, status %s, runner %s", err, res.Status, res.Runner)
	}
}
