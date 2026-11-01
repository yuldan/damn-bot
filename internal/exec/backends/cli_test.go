//go:build !minimal && unix

package backends

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
)

// fakeCLI links a pre-written, immutable CLI helper into the test dir. A
// per-test script would be open for writing while other parallel tests exec
// their own copies; Linux then reports ETXTBSY even for distinct files. The
// template lives for the test process; the OS reclaims the small temp dir.
//
//nolint:gochecknoglobals // one immutable helper is shared by every parallel test in the process
var (
	fakeCLIOnce sync.Once
	fakeCLIDir  string
	fakeCLIErr  error
)

func fakeCLIBinary() (string, error) {
	fakeCLIOnce.Do(func() {
		dir, err := os.MkdirTemp("", "damnbot-fake-cli-")
		if err != nil {
			fakeCLIErr = err
			return
		}
		fakeCLIDir = dir
		// The log paths are derived from $0's directory, so one immutable
		// script serves every test.
		script := `#!/bin/sh
dir=$(dirname "$0")
image=${0##*/}
image=${image#fake-cli-}
printf '%s\n' "$@" >> "$dir/argv.log"
prev=''
for a in "$@"; do
  if [ "$prev" = "--env-file" ]; then cp "$a" "$dir/env.log" 2>/dev/null || true; fi
  prev="$a"
done
found=0
while [ $# -gt 0 ]; do
  if [ "$1" = "$image" ]; then found=1; shift; break; fi
  shift
done
if [ "$found" = 0 ]; then exit 0; fi
exec "$@"
`
		bin := filepath.Join(dir, "fake-cli")
		tmp := filepath.Join(dir, "fake-cli.tmp")
		//nolint:gosec // the fake CLI must be executable by the test
		if err := os.WriteFile(tmp, []byte(script), 0o700); err != nil {
			fakeCLIErr = err
			return
		}
		if err := os.Rename(tmp, bin); err != nil {
			fakeCLIErr = fmt.Errorf("install fake CLI: %w", err)
		}
	})
	if fakeCLIErr != nil {
		return "", fakeCLIErr
	}
	return filepath.Join(fakeCLIDir, "fake-cli"), nil
}

// fakeCLI hardlinks the immutable helper into a fresh test dir and returns
// its argv/env logs. The link name carries the image argv marker.
func fakeCLI(t *testing.T, image string) (bin, logPath, envPath string) {
	t.Helper()
	src, err := fakeCLIBinary()
	if err != nil {
		t.Fatalf("prepare fake CLI: %v", err)
	}
	dir := t.TempDir()
	bin = filepath.Join(dir, "fake-cli-"+image)
	if err := os.Link(src, bin); err != nil {
		t.Fatalf("link fake CLI: %v", err)
	}
	return bin, filepath.Join(dir, "argv.log"), filepath.Join(dir, "env.log")
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	//nolint:gosec // the path is a test temp file
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return string(b)
}

// TestDockerRunContract: argv hardening, exit code, output, env file, and
// container cleanup of the docker CLI backend.
func TestDockerRunContract(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// secureRoot canonicalizes the workspace (/var -> /private/var on macOS);
	// the mount must name the real path, so compare against the same form.
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	bin, argvLog, envLog := fakeCLI(t, "fake-image")
	d, err := NewDocker(&DockerConfig{
		Binary: bin, Image: "fake-image", User: "65534:65534", Workspace: root,
		CPUMilli: 1500, MemoryMiB: 256, PIDs: 64, Network: "none",
		EnvAllow: []string{"DAMNBOT_TEST"},
	})
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	t.Cleanup(func() {
		//nolint:errcheck,gosec // test cleanup
		d.Close(context.Background())
	})

	res, err := d.Run(t.Context(), &exec.Spec{
		Argv: []string{"/bin/sh", "-c", "printf 'out:%s' \"$DAMNBOT_TEST\"; exit 3"},
		Dir:  root, Env: []string{"DAMNBOT_TEST=hello"}, Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != exec.StatusExited || res.ExitCode == nil || *res.ExitCode != 3 {
		t.Fatalf("status = %s exit = %v", res.Status, res.ExitCode)
	}
	if got := string(res.Output); strings.Contains(got, "hello") {
		// The fake CLI does not read the env file; the container would.
		t.Fatalf("host process saw the container environment: %q", got)
	}
	argv := readLog(t, argvLog)
	for _, want := range []string{
		"run", "--rm", "-i", "--network=none", "--pull=never", "--cap-drop=ALL",
		"--security-opt=no-new-privileges", "--user", "65534:65534",
		"--cpus=1.500", "--memory=256m", "--pids-limit=64",
		"type=bind,src=" + root + ",dst=" + root + ",rw", "--workdir",
		"fake-image", "rm", "-f",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv missing %q:\n%s", want, argv)
		}
	}
	if strings.Contains(argv, "docker.sock") {
		t.Errorf("argv exposes docker.sock:\n%s", argv)
	}
	env := readLog(t, envLog)
	if !strings.Contains(env, "DAMNBOT_TEST=hello") {
		t.Fatalf("env file = %q", env)
	}
}

// TestDockerRejectsEscapes: cwd outside the mount, secret-like env, and a
// workspace holding docker.sock are refused before any container starts.
func TestDockerRejectsEscapes(t *testing.T) {
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
		t.Fatalf("outside cwd = %v", err)
	}
	if _, err := d.Run(t.Context(), &exec.Spec{
		Argv: []string{"/bin/true"}, Dir: root, Env: []string{"OPENAI_API_KEY=sk-x"},
	}); !errors.Is(err, exec.ErrCapability) {
		t.Fatalf("secret env = %v", err)
	}
	if _, err := d.Run(t.Context(), &exec.Spec{Argv: []string{"/bin/true"}, Limits: exec.Limits{PIDs: 1}, Dir: root}); err != nil {
		// limits are supported, not rejected; this call must reach the fake CLI.
		t.Fatalf("limits: %v", err)
	}

	sockDir := t.TempDir()
	sockPath := filepath.Join(sockDir, "docker.sock")
	lc := net.ListenConfig{}
	l, lerr := lc.Listen(t.Context(), "unix", sockPath)
	if lerr != nil {
		t.Skipf("unix socket: %v", lerr)
	}
	t.Cleanup(func() {
		//nolint:errcheck,gosec // test listener cleanup
		l.Close()
	})
	if _, err := NewDocker(&DockerConfig{
		Binary: bin, Image: "fake-image", User: "65534:65534", Workspace: sockDir, Network: "none",
	}); !errors.Is(err, exec.ErrCapability) {
		t.Fatalf("docker.sock workspace = %v", err)
	}
	if _, err := NewDocker(&DockerConfig{
		Binary: bin, Image: "fake-image", User: "0:0", Workspace: root, Network: "none",
	}); !errors.Is(err, exec.ErrSpec) {
		t.Fatalf("root user = %v", err)
	}
}

// TestDockerTimeoutCleansContainer: a timed-out call returns timeout and the
// container is removed (no orphan), including Sweep by exec id.
func TestDockerTimeoutCleansContainer(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin, argvLog, _ := fakeCLI(t, "fake-image")
	d, err := NewDocker(&DockerConfig{
		Binary: bin, Image: "fake-image", User: "65534:65534", Workspace: root, Network: "none",
	})
	if err != nil {
		t.Fatalf("NewDocker: %v", err)
	}
	res, err := d.Run(t.Context(), &exec.Spec{
		Argv: []string{"/bin/sh", "-c", "sleep 30"}, Dir: root,
		Timeout: 300 * time.Millisecond, KillGrace: time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != exec.StatusTimeout {
		t.Fatalf("status = %s", res.Status)
	}
	if argv := readLog(t, argvLog); !strings.Contains(argv, "rm\n-f\n") {
		t.Fatalf("cleanup not issued:\n%s", argv)
	}
	if _, err := d.Sweep(t.Context(), "1.abc"); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
}
