//go:build unix

package backends

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
)

// fakeCLI writes a shell script that logs its argv (and any --env-file) and
// then runs the argv after the fake image name, emulating the container.
func fakeCLI(t *testing.T, image string) (bin, logPath, envPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "argv.log")
	envPath = filepath.Join(dir, "env.log")
	bin = filepath.Join(dir, "fake-cli")
	script := `#!/bin/sh
printf '%s\n' "$@" >> ` + shellQuote(logPath) + `
prev=''
for a in "$@"; do
  if [ "$prev" = "--env-file" ]; then cp "$a" ` + shellQuote(envPath) + ` 2>/dev/null || true; fi
  prev="$a"
done
found=0
while [ $# -gt 0 ]; do
  if [ "$1" = ` + shellQuote(image) + ` ]; then found=1; shift; break; fi
  shift
done
if [ "$found" = 0 ]; then exit 0; fi
exec "$@"
`
	//nolint:gosec // the fake CLI must be executable by the test
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	return bin, logPath, envPath
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

// TestSingularityArgvAndCaps: containall/bind/network none and a truthful
// capability set (no resource limits, no PTY).
func TestSingularityArgvAndCaps(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin, argvLog, _ := fakeCLI(t, "fake.sif")
	s, err := NewSingularity(&SingularityConfig{
		Binary: bin, Image: "fake.sif", Workspace: root, Network: "none",
	})
	if err != nil {
		t.Fatalf("NewSingularity: %v", err)
	}
	res, err := s.Run(t.Context(), &exec.Spec{
		Argv: []string{"/bin/sh", "-c", "echo ok"}, Dir: root, Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != exec.StatusExited || !strings.Contains(string(res.Output), "ok") {
		t.Fatalf("status = %s output = %q argv=%q", res.Status, res.Output, readLog(t, argvLog))
	}
	argv := readLog(t, argvLog)
	for _, want := range []string{"exec", "--containall", "--no-home", "--cleanenv", "--net", "--network\nnone", "--bind", root + ":" + root} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv missing %q:\n%s", want, argv)
		}
	}
	if s.Caps().ResourceLimits {
		t.Fatal("singularity must not claim resource limits")
	}
	if _, err := s.Run(t.Context(), &exec.Spec{Argv: []string{"/bin/true"}, Limits: exec.Limits{MemoryMiB: 32}}); !errors.Is(err, exec.ErrCapability) {
		t.Fatalf("limits = %v", err)
	}
	if _, err := s.Run(t.Context(), &exec.Spec{Argv: []string{"/bin/true"}, PTY: true}); !errors.Is(err, exec.ErrCapability) {
		t.Fatalf("pty = %v", err)
	}
}

// TestDockerSymlinkEscapeAndAllowlist: a cwd symlink pointing outside the
// mount is refused, and an allowlist network requires the filtering proxy.
func TestDockerSymlinkEscapeAndAllowlist(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	bin, argvLog, _ := fakeCLI(t, "fake-image")
	d, err := NewDocker(&DockerConfig{
		Binary: bin, Image: "fake-image", User: "65534:65534", Workspace: root,
		Network: "allowlist", NetworkName: "damnbot-egress",
		EgressAllow: []string{"proxy.golang.org:443"}, ProxyURL: "http://127.0.0.1:8080",
	})
	if err != nil {
		t.Fatalf("NewDocker allowlist: %v", err)
	}
	if _, err := d.Run(t.Context(), &exec.Spec{Argv: []string{"/bin/true"}, Dir: link}); !errors.Is(err, exec.ErrCapability) {
		t.Fatalf("symlink cwd = %v", err)
	}
	if _, err := d.Run(t.Context(), &exec.Spec{Argv: []string{"/bin/true"}, Dir: root}); err != nil {
		t.Fatalf("allowlist run: %v", err)
	}
	argv := readLog(t, argvLog)
	if !strings.Contains(argv, "HTTPS_PROXY=http://127.0.0.1:8080") || !strings.Contains(argv, "--network=damnbot-egress") {
		t.Fatalf("allowlist argv:\n%s", argv)
	}
	if _, err := NewDocker(&DockerConfig{
		Binary: bin, Image: "fake-image", User: "65534:65534", Workspace: root,
		Network: "allowlist",
	}); !errors.Is(err, exec.ErrCapability) {
		t.Fatalf("allowlist without proxy = %v", err)
	}
}
