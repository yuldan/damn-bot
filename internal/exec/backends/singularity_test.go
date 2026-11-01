//go:build !minimal && unix

package backends

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
)

func newTestSingularity(t *testing.T, binary, root string) *Singularity {
	t.Helper()
	s, err := NewSingularity(&SingularityConfig{
		Binary: binary, Image: "fake.sif", Workspace: root,
		CPUMilli: 1500, MemoryMiB: 256, PIDs: 64, Network: "none", EnvAllow: []string{"LANG"},
	})
	if err != nil {
		t.Fatalf("NewSingularity: %v", err)
	}
	return s
}

// TestSingularityRunContract: the CLI gets containment flags, the workspace
// bound at the same path, cgroup limits, and the allowlisted environment via
// the APPTAINERENV_ channel (never argv).
func TestSingularityRunContract(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// secureRoot canonicalizes the workspace (/var -> /private/var on macOS);
	// the bind must name the real path, so compare against the same form.
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	bin, argvLog, _ := fakeCLI(t, "fake.sif")
	s := newTestSingularity(t, bin, root)
	res, err := s.Run(t.Context(), &exec.Spec{
		Argv: []string{"/bin/sh", "-c", "printf run-ok; exit 7"},
		Dir:  root, Env: []string{"LANG=C"}, Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != exec.StatusExited || res.ExitCode == nil || *res.ExitCode != 7 || string(res.Output) != "run-ok" {
		t.Fatalf("result = %+v", res)
	}
	argv := readLog(t, argvLog)
	for _, want := range []string{
		"exec", "--containall", "--cleanenv", "--bind", root + ":" + root, "--pwd", root,
		"--net", "--network", "none", "--cpus", "1.500", "--memory", "256M", "--pids-limit", "64", "fake.sif",
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv missing %q:\n%s", want, argv)
		}
	}
	if strings.Contains(argv, "LANG=C") {
		t.Errorf("environment reached argv:\n%s", argv)
	}
	env := s.childEnv("1.abc", []string{"LANG=C"})
	if !slices.Contains(env, "APPTAINERENV_LANG=C") || !slices.Contains(env, exec.MarkerEnv+"=1.abc") {
		t.Errorf("child env = %v", env)
	}
}

// TestSingularityEnvAndRejections: host APPTAINERENV_ leaks are dropped, an
// out-of-workspace cwd and a PTY are refused, and timeout kills the tree.
func TestSingularityEnvAndRejections(t *testing.T) {
	root := t.TempDir()
	bin, _, _ := fakeCLI(t, "fake.sif")
	s := newTestSingularity(t, bin, root)
	t.Setenv("APPTAINERENV_SECRET", "leak")
	if env := s.childEnv("2.x", nil); slices.Contains(env, "APPTAINERENV_SECRET=leak") {
		t.Fatalf("host APPTAINERENV_ leak kept: %v", env)
	}
	if _, err := s.Run(t.Context(), &exec.Spec{Argv: []string{"true"}, Dir: "/tmp"}); !errors.Is(err, exec.ErrCapability) {
		t.Fatalf("outside cwd = %v", err)
	}
	if _, err := s.Run(t.Context(), &exec.Spec{Argv: []string{"true"}, Dir: root, PTY: true}); !errors.Is(err, exec.ErrCapability) {
		t.Fatalf("pty = %v", err)
	}
	res, err := s.Run(t.Context(), &exec.Spec{Argv: []string{"/bin/sh", "-c", "sleep 30"}, Dir: root, Timeout: 200 * time.Millisecond})
	if err != nil || res.Status != exec.StatusTimeout {
		t.Fatalf("timeout = %+v, %v", res, err)
	}
	if n, serr := s.Sweep(t.Context(), res.ExecID); serr != nil || n != 0 {
		t.Fatalf("Sweep = %d, %v", n, serr)
	}
	if err := s.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestSingularityValidation: allowlist mode needs its isolated network,
// proxy and hosts; the workspace cannot smuggle a bind separator.
func TestSingularityValidation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	base := SingularityConfig{Binary: "apptainer", Image: "img.sif", Workspace: root, Network: "allowlist"}
	if _, err := NewSingularity(&base); !errors.Is(err, exec.ErrCapability) {
		t.Fatalf("allowlist without egress = %v", err)
	}
	ok := base
	ok.NetworkName, ok.ProxyURL, ok.EgressAllow = "isolated", "http://127.0.0.1:8080", []string{"proxy.golang.org:443"}
	s, err := NewSingularity(&ok)
	if err != nil {
		t.Fatalf("allowlist: %v", err)
	}
	argv := s.argv(root, &exec.Spec{Argv: []string{"true"}})
	if !slices.Contains(argv, "isolated") {
		t.Fatalf("allowlist network missing: %v", argv)
	}
	if env := s.childEnv("3.x", nil); !slices.Contains(env, "APPTAINERENV_HTTPS_PROXY=http://127.0.0.1:8080") {
		t.Fatalf("proxy env missing: %v", env)
	}
	if _, err := NewSingularity(&SingularityConfig{Binary: "apptainer", Image: "i", Workspace: root, Network: "host"}); !errors.Is(err, exec.ErrSpec) {
		t.Fatalf("network host = %v", err)
	}
}
