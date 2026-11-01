package e2e

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// fakeContainerCLI writes a CLI that logs every argv and then runs the argv
// after the image name, emulating `apptainer exec`/`docker run` without a
// daemon: the real binary's shell tool still crosses the backend adapter.
func fakeContainerCLI(t *testing.T, dir, image string) (binary, logPath string) {
	t.Helper()
	logPath = filepath.Join(dir, "argv.log")
	binary = filepath.Join(dir, "fake-container")
	script := `#!/bin/sh
printf '%s\n' "$@" >> ` + shellQuote(logPath) + `
found=0
for a in "$@"; do
  if [ "$a" = ` + shellQuote(image) + ` ]; then found=1; shift; break; fi
  shift
done
if [ "$found" = 0 ]; then exit 0; fi
exec "$@"
`
	// Rename after writing so a concurrent fork cannot inherit a write
	// descriptor for the script and make execve fail with ETXTBSY.
	tmp := binary + ".tmp"
	//nolint:gosec // the fake CLI must be executable by the test
	if err := os.WriteFile(tmp, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}
	if err := os.Rename(tmp, binary); err != nil {
		t.Fatalf("rename fake CLI: %v", err)
	}
	return binary, logPath
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// TestExecBackendAcrossBackends drives one shell call through local, docker and
// singularity; a cloud backend without its secret must fail closed.
//
//nolint:gocognit // the table is the scenario
func TestExecBackendAcrossBackends(t *testing.T) {
	dir := t.TempDir()
	dockerBin, dockerLog := fakeContainerCLI(t, dir, "fake-image")
	singBin, singLog := fakeContainerCLI(t, dir, "fake.sif")
	cases := []struct {
		name, config, log, marker string
	}{
		{"local", "", "", "e2e-local"},
		{"docker", "[exec]\nbackend = \"docker\"\n[exec.backends.docker]\nbinary = " + shellQuote(dockerBin) +
			"\nimage = \"fake-image\"\n", dockerLog, "e2e-docker"},
		{"singularity", "[exec]\nbackend = \"singularity\"\n[exec.backends.singularity]\nbinary = " + shellQuote(singBin) +
			"\nimage = \"fake.sif\"\n", singLog, "e2e-singularity"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.log != "" && runtime.GOOS == "windows" {
				// The fake container CLI is a sh script; docker/singularity
				// lanes are unix-only (cli_test.go build tag), so the Windows
				// lane has nothing to drive.
				t.Skip("fake container CLI needs a POSIX shell")
			}
			e := NewTrusted(t,
				ToolCallReply("call-1", "shell", `{"command":"printf `+tc.marker+`"}`),
				providertest.Text("done"))
			if tc.config != "" {
				if err := os.WriteFile(e.cfg, []byte(tc.config), 0o600); err != nil {
					t.Fatalf("write config: %v", err)
				}
			}
			res := e.Run("run it", "--approval", "auto", "--unsafe-auto-shell")
			res.RequireOK(t)
			if !strings.Contains(e.RequestText(t, 1), tc.marker) {
				t.Fatalf("tool result missing %q:\n%s", tc.marker, e.RequestText(t, 1))
			}
			if tc.log != "" {
				raw, err := os.ReadFile(tc.log)
				if err != nil {
					t.Fatalf("read backend argv: %v", err)
				}
				if !strings.Contains(string(raw), tc.marker) || (tc.name == "singularity" && !strings.Contains(string(raw), "--containall")) {
					t.Fatalf("%s argv = %q", tc.name, raw)
				}
			}
		})
	}
}

// TestExecBackendCloudFailsClosed: a selected cloud backend without its
// keychain secret is module_unavailable, never a silent local run.
func TestExecBackendCloudFailsClosed(t *testing.T) {
	e := NewTrusted(t,
		ToolCallReply("call-1", "shell", `{"command":"printf e2e-cloud"}`),
		providertest.Text("done"))
	cfg := "[exec]\nbackend = \"vercel_sandbox\"\n[exec.backends.vercel_sandbox]\ntoken = \"damnbot-e2e-missing\"\nruntime = \"node24\"\n"
	if err := os.WriteFile(e.cfg, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	res := e.Run("run it", "--approval", "auto", "--unsafe-auto-shell")
	res.RequireOK(t)
	found := false
	for i := range len(e.Requests()) {
		if strings.Contains(e.RequestText(t, i), "module_unavailable") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("cloud backend did not fail closed:\n%s", e.RequestText(t, len(e.Requests())-1))
	}
}
