//go:build !minimal && unix

package backends

import (
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestContainerCwdValidated (EC-FIO-003): a container backend must refuse a
// host cwd outside the mounted workspace before the CLI runs, so a file tool
// can never search an empty container at the host path.
func TestContainerCwdValidated(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-FIO-003", "F5", "covered") {
		return
	}
	t.Run("EC-FIO-003", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		bin, _, _ := fakeCLI(t, "fake-image")
		d, err := NewDocker(&DockerConfig{
			Binary: bin, Image: "fake-image", User: "65534:65534", Workspace: root, Network: "none",
		})
		if err != nil {
			t.Fatalf("NewDocker: %v", err)
		}
		if _, err := d.Run(t.Context(), &exec.Spec{Argv: []string{"/bin/true"}, Dir: "/tmp"}); !errors.Is(err, exec.ErrCapability) {
			t.Fatalf("host cwd outside mount = %v, want ErrCapability", err)
		}
	})
}
