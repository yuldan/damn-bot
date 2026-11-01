//go:build docker && unix

package backends

import (
	"context"
	"os"
	osexec "os/exec"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
)

// TestDockerRealDaemon is the optional real-daemon check (CI job with a
// docker socket and DAMNBOT_DOCKER_IMAGE set). It is excluded from the
// default gate by the `docker` build tag.
func TestDockerRealDaemon(t *testing.T) {
	if _, err := osexec.LookPath("docker"); err != nil {
		t.Skip("docker CLI absent")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := osexec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").Run(); err != nil {
		t.Skip("docker daemon unavailable")
	}
	image := os.Getenv("DAMNBOT_DOCKER_IMAGE")
	if image == "" {
		t.Skip("set DAMNBOT_DOCKER_IMAGE to a locally pulled image")
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	d, err := NewDocker(&DockerConfig{
		Binary: "docker", Image: image, User: "65534:65534", Workspace: root,
		CPUMilli: 500, MemoryMiB: 128, PIDs: 64, Network: "none",
	})
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	t.Cleanup(func() { _ = d.Close(context.Background()) })
	res, err := d.Run(t.Context(), &exec.Spec{
		Argv: []string{"/bin/sh", "-c", "id -u; echo hello"}, Dir: root, Timeout: 60 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != exec.StatusExited || !strings.Contains(string(res.Output), "hello") {
		t.Fatalf("status = %s output = %q", res.Status, res.Output)
	}
	if strings.Contains(string(res.Output), "\n0\n") {
		t.Fatalf("container ran as root: %q", res.Output)
	}
}
