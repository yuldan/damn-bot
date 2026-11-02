//go:build unix

package backends

import (
	"context"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestECTERM003 (EC-TERM-003, F5): a non-local backend replaces host
// execution and the result says so; local does not, and no host path leaks.
func TestECTERM003(t *testing.T) {
	g := phase.Current(t)
	if !g.Item(t, "EC-TERM-003", "F5", "covered") {
		return
	}
	t.Run("local is host", func(t *testing.T) {
		l := exec.NewLocal(exec.New(t.Context()))
		defer func() {
			//nolint:errcheck,gosec // test cleanup
			l.Close(context.Background())
		}()
		res, err := l.Run(t.Context(), &exec.Spec{Argv: []string{"/bin/echo", "hi"}})
		if err != nil || res.Execution.Sandboxed || res.Execution.Backend != "local" {
			t.Fatalf("local execution = %+v err=%v", res.Execution, err)
		}
	})
	t.Run("docker is sandbox", func(t *testing.T) {
		root := t.TempDir()
		bin, _, _ := fakeCLI(t, "fake-image")
		d, err := NewDocker(&DockerConfig{Binary: bin, Image: "fake-image", User: "65534:65534", Workspace: root, Network: "none"})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			//nolint:errcheck,gosec // test cleanup
			d.Close(context.Background())
		}()
		res, err := d.Run(t.Context(), &exec.Spec{Argv: []string{"/bin/echo", "hi"}, Dir: root, Timeout: 5e9})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Execution.Sandboxed || res.Execution.Backend != "docker" || res.Execution.Detail == "" {
			t.Fatalf("docker execution = %+v", res.Execution)
		}
		if strings.Contains(res.Execution.Detail, root) || strings.Contains(string(res.Output), root) {
			t.Fatalf("host path leaked: %+v %q", res.Execution, res.Output)
		}
	})
	t.Run("ssh is remote", func(t *testing.T) {
		srv := startSSHServer(t)
		b := newTestSSH(t, srv)
		res, err := b.Run(t.Context(), &exec.Spec{Argv: []string{"/bin/true"}, Dir: "/tmp", Timeout: 5e9})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Execution.Sandboxed || res.Execution.Backend != "ssh" {
			t.Fatalf("ssh execution = %+v", res.Execution)
		}
	})
}
