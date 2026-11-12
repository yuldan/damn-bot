//go:build !minimal && unix

package backends

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
)

// TestDockerBackendNoSocketMount (GAP-SEC-22, F9 ADR-347) proves the docker
// exec backend never passes the host container socket to a sandbox: the
// generated argv contains no docker.sock path, and a workspace that holds the
// socket is refused before any container starts.
func TestDockerBackendNoSocketMount(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin, argvLog, _ := fakeCLI(t, "fake-image")
	d, err := NewDocker(&DockerConfig{
		Binary: bin, Image: "fake-image", User: "65534:65534", Workspace: root, Network: "none",
	})
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	if _, err := runBackend(t, d, &exec.Spec{Argv: []string{"/bin/true"}, Dir: root, Timeout: 10 * time.Second}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if argv := readLog(t, argvLog); containsSocket(argv) {
		t.Fatalf("docker argv mounts the host socket:\n%s", argv)
	}
	if _, err := NewDocker(&DockerConfig{
		Binary: bin, Image: "fake-image", User: "65534:65534", Workspace: socketDir(t), Network: "none",
	}); err == nil {
		t.Fatal("workspace dengan docker.sock diterima; harus exec.ErrCapability")
	}
}

func containsSocket(argv string) bool { return strings.Contains(argv, "docker.sock") }

// socketDir creates a directory containing a live docker.sock placeholder.
func socketDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	lc := net.ListenConfig{}
	l, err := lc.Listen(t.Context(), "unix", filepath.Join(dir, "docker.sock"))
	if err != nil {
		t.Skipf("unix socket tidak tersedia: %v", err)
	}
	t.Cleanup(func() {
		//nolint:errcheck,gosec // listener cleanup pada test
		l.Close()
	})
	return dir
}
