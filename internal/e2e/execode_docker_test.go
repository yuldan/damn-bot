//go:build linux && !minimal && e2e_docker

package e2e

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// dockerOrFail returns the image for the positive execute_code path: local
// run skips, CI fails instead of skipping silently (network isolation is
// only real through an isolating backend).
func dockerOrFail(t *testing.T) string {
	t.Helper()
	image := os.Getenv("DAMNBOT_E2E_DOCKER_IMAGE")
	_, err := exec.LookPath("docker")
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").Run()
	}
	if err == nil && image != "" {
		return image
	}
	skipOrFail(t, fmt.Sprintf("docker/image unavailable: %v image=%q", err, image))
	return ""
}

// TestExecuteCodeDockerPositive runs only in the CI fixture job (tag
// e2e_docker), which builds internal/e2e/testdata and exports
// DAMNBOT_E2E_DOCKER_IMAGE. dockerOrFail keeps skipOrFail hard: in CI a
// missing image or docker daemon fails instead of hiding the path.
func TestExecuteCodeDockerPositive(t *testing.T) {
	t.Parallel()
	image := dockerOrFail(t)
	code := `{"language":"python","code":"print('e2e-code-ok')"}`
	e := NewTrusted(t, ToolCallReply("c1", "execute_code", code), providertest.Text("done"))
	writeExecConfig(t, e, "[exec]\nbackend = \"docker\"\ncode_auto_languages = [\"python\"]\n[exec.backends.docker]\nbinary = \"docker\"\nimage = \""+image+"\"\nuser = \"65534:65534\"\nnetwork = \"none\"\n")
	res := e.Run("run code", "--approval", "auto")
	if res.Code != 0 || !strings.Contains(e.RequestText(t, 1), "e2e-code-ok") {
		t.Fatalf("docker execute_code failed: %+v", res)
	}
}
