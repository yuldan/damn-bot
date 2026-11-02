//go:build linux && !minimal && e2e_docker

package e2e

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

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
