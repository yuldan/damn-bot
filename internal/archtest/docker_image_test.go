//go:build e2e_docker

package archtest

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// dockerConfig is the subset of `docker image inspect` used below.
type dockerConfig struct {
	User        string   `json:"User"`
	Entrypoint  []string `json:"Entrypoint"`
	Healthcheck *struct {
		Test []string `json:"Test"`
	} `json:"Healthcheck"`
}

// TestDockerImageSize (GAP-INF-03, ADR-033 Q49) builds the shipped Dockerfile
// and checks the measured image: uncompressed size ≤ release binary + 2 MB,
// UID 65532, exec-form healthcheck, and no shell/compiler in the runtime
// image. It is compiled only under `-tags e2e_docker`; the CI job sets
// DAMNBOT_REQUIRE_DOCKER=1 so a missing daemon fails instead of skipping.
func TestDockerImageSize(t *testing.T) {
	t.Parallel()
	docker, ok := dockerOrFail(t)
	if !ok {
		return
	}
	bin := filepath.Join(t.TempDir(), "damnbot")
	run(t, repoDir(), "go", "build", "-trimpath", "-tags", "goolm", "-ldflags=-s -w", "-o", bin, "./cmd/damnbot")
	binInfo, err := os.Stat(bin)
	if err != nil {
		t.Fatalf("stat binary: %v", err)
	}

	image := "damnbot-image-test:ci"
	run(t, repoDir(), docker, "build", "-t", image, ".")

	out := run(t, repoDir(), docker, "image", "inspect", "--format", "{{.Size}}", image)
	size, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		t.Fatalf("docker image size %q: %v", out, err)
	}
	const extra = 2_000_000 // SPEC GAP-INF-03: image ≤ binary + 2 MB
	if max := binInfo.Size() + extra; size > max {
		t.Fatalf("image %d B > binary %d B + 2 MB = %d B (GAP-INF-03)", size, binInfo.Size(), max)
	}
	t.Logf("image %d B ≤ binary %d B + 2 MB (%d B)", size, binInfo.Size(), binInfo.Size()+extra)

	var cfg dockerConfig
	raw := run(t, repoDir(), docker, "image", "inspect", "--format", "{{json .Config}}", image)
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &cfg); err != nil {
		t.Fatalf("docker image config: %v\n%s", err, raw)
	}
	if cfg.User != "65532:65532" {
		t.Errorf("image user = %q, want 65532:65532 (GAP-SEC-22)", cfg.User)
	}
	if len(cfg.Entrypoint) != 1 || cfg.Entrypoint[0] != "/damnbot" {
		t.Errorf("entrypoint = %v, want [/damnbot]", cfg.Entrypoint)
	}
	if cfg.Healthcheck == nil || len(cfg.Healthcheck.Test) < 5 ||
		cfg.Healthcheck.Test[0] != "CMD" || cfg.Healthcheck.Test[1] != "/damnbot" ||
		cfg.Healthcheck.Test[2] != "doctor" || cfg.Healthcheck.Test[3] != "health" ||
		cfg.Healthcheck.Test[4] != "--ready" {
		t.Errorf("healthcheck = %+v, want CMD /damnbot doctor health --ready", cfg.Healthcheck)
	}

	// The image runs without a shell and starts via the static binary.
	if got := run(t, repoDir(), docker, "run", "--rm", image, "--version"); !strings.Contains(got, "damnbot") {
		t.Errorf("docker run --version = %q, want damnbot version", got)
	}
	for _, probe := range [][]string{
		{"run", "--rm", "--entrypoint", "/bin/sh", image, "-c", "true"},
		{"run", "--rm", "--entrypoint", "/usr/bin/gcc", image, "--version"},
	} {
		if out, err := exec.Command(docker, probe...).CombinedOutput(); err == nil {
			t.Fatalf("runtime image menyediakan %q: %s", probe[3], out)
		}
	}
}

// dockerOrFail returns docker when the daemon answers. Outside CI (`e2e_docker`
// without DAMNBOT_REQUIRE_DOCKER=1) an absent daemon skips locally but fail-closes
// in the required job.
func dockerOrFail(t *testing.T) (string, bool) {
	t.Helper()
	require := os.Getenv("DAMNBOT_REQUIRE_DOCKER") == "1"
	docker, err := exec.LookPath("docker")
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err = exec.CommandContext(ctx, docker, "info", "--format", "{{.ServerVersion}}").Run()
	}
	if err != nil {
		if require {
			t.Fatalf("DAMNBOT_REQUIRE_DOCKER=1: docker tidak tersedia: %v", err)
		}
		t.Skipf("docker tidak tersedia (set DAMNBOT_REQUIRE_DOCKER=1 untuk mewajibkan): %v", err)
		return "", false
	}
	return docker, true
}

// run executes a command in dir with a 15-minute cap (image build ≤ 5 min per
// SPEC; cold module downloads get headroom) and fails with combined output.
func run(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}
