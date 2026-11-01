//go:build !minimal && unix

package backends

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
)

// TestCLITransportContract is the shared transport of the CLI backends:
// spawn, exit code, combined output, timeout tree kill.
func TestCLITransportContract(t *testing.T) {
	t.Parallel()
	res, err := RunCLI(t.Context(), "test", &CLISpec{
		argv: []string{"/bin/sh", "-c", "printf out; exit 4"}, timeout: 5 * time.Second,
	})
	if err != nil || res.Status != exec.StatusExited || res.ExitCode == nil || *res.ExitCode != 4 || string(res.Output) != "out" {
		t.Fatalf("run: %+v err=%v", res, err)
	}
	res, err = RunCLI(t.Context(), "test", &CLISpec{
		argv: []string{"/bin/sh", "-c", "sleep 30"}, timeout: 200 * time.Millisecond, grace: time.Second,
	})
	if err != nil || res.Status != exec.StatusTimeout {
		t.Fatalf("timeout: %+v err=%v", res, err)
	}
	if _, err := EnvList([]string{"OPENAI_API_KEY=x"}, nil); !errors.Is(err, exec.ErrCapability) {
		t.Fatalf("secret env = %v", err)
	}
	if got, err := EnvList([]string{"PATH=/bin"}, []string{"OPENAI_API_KEY"}); err != nil || len(got) != 1 {
		t.Fatalf("allowed env = %v, %v", got, err)
	}
	if ShellQuote("a'b") != `'a'\''b'` {
		t.Fatalf("quote = %s", ShellQuote("a'b"))
	}
	if _, err := ResolveBinary("definitely-not-a-binary-xyz"); !errors.Is(err, exec.ErrUnavailable) {
		t.Fatalf("resolve = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := RunCLI(ctx, "test", &CLISpec{argv: []string{"/bin/sleep", "30"}}); err == nil {
		t.Fatal("cancelled context: want error")
	}
}
