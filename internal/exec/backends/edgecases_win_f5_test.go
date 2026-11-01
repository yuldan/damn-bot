//go:build !minimal && windows

package backends

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestDockerVolumeDriveLetter (EC-WIN-007): a Windows workspace is mounted
// with the drive-letter src and the /c/... container destination; UNC paths
// fail with a clear capability error instead of an empty mount.
func TestDockerVolumeDriveLetter(t *testing.T) {
	g := phase.Current(t)
	if !g.Item(t, "EC-WIN-007", "F5", "covered") {
		return
	}
	for _, tc := range []struct{ in, want string }{
		{`C:\work`, "/c/work"},
		{`D:/projects/app`, "/d/projects/app"},
		{`C:\`, "/c"},
		{`\\?\C:\work`, "/c/work"},
	} {
		got, err := containerPath(tc.in)
		if err != nil || got != tc.want {
			t.Fatalf("containerPath(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, unc := range []string{`\\server\share`, `\\?\UNC\server\share`} {
		if _, err := containerPath(unc); !errors.Is(err, exec.ErrCapability) {
			t.Fatalf("UNC path %q = %v, want ErrCapability", unc, err)
		}
	}
	root := t.TempDir()
	real, rerr := filepath.EvalSymlinks(root)
	if rerr != nil {
		real = root
	}
	d, err := NewDocker(&DockerConfig{Binary: "docker", Image: "img", User: "65534:65534", Workspace: root, Network: "none"})
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	// secureRoot/insideRoot resolve the workspace and cwd, so the container dst
	// comes from the resolved path; feeding the raw short path here would make
	// workdir disagree with the mount.
	argv, _, err := d.argv("1.x", "name", real, &exec.Spec{Argv: []string{"true"}}, nil)
	if err != nil {
		t.Fatalf("argv: %v", err)
	}
	joined := strings.Join(argv, "\n")
	dst, cerr := containerPath(real)
	if cerr != nil || !strings.Contains(joined, "dst="+dst) || !strings.Contains(joined, "--workdir\n"+dst) {
		t.Fatalf("mount/workdir missing dst %q:\n%s", dst, joined)
	}
	if strings.Contains(joined, "dst="+real) || strings.Contains(joined, "dst="+root) {
		t.Fatalf("raw Windows path used as container dst:\n%s", joined)
	}
}
