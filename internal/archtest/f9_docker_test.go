package archtest

import (
	"strings"
	"testing"
)

// TestF9OPS029DockerCompose (F9 ledger OPS-029, ADR-347) proves the shipped
// image/compose pair: the Dockerfile runtime stage is distroless with the
// REL-17 healthcheck and the official compose file is non-root, read-only and
// socket-free.
func TestF9OPS029DockerCompose(t *testing.T) {
	t.Parallel()
	stages := parseDockerfile(t, readRepoFile(t, "Dockerfile"))
	checkDockerRuntimeStage(t, stages[len(stages)-1])
	checkCompose(t, readRepoFile(t, "docker-compose.yml"))
}

// TestDockerImageNonRoot (GAP-SEC-22) asserts the runtime identity and that the
// final stage has no shell/compiler to exec.
func TestDockerImageNonRoot(t *testing.T) {
	t.Parallel()
	final := finalDockerStage(t)
	if user := stageInstruction(final, "USER"); user == nil || len(user.Arg) != 1 || user.Arg[0] != "65532:65532" {
		t.Fatalf("USER runtime = %v, want 65532:65532", user)
	}
	checkDockerNoExecutor(t, final)
	if entry := stageInstruction(final, "ENTRYPOINT"); entry == nil || !strings.Contains(entry.Raw, `["/damnbot"]`) {
		t.Fatalf("ENTRYPOINT = %v, want [\"/damnbot\"]", entry)
	}
}

// TestDockerHealthcheck (GAP-INF-03/REL-17) asserts the image healthcheck is the
// same readiness command as `damnbot serve` (exec form, no shell).
func TestDockerHealthcheck(t *testing.T) {
	t.Parallel()
	checkDockerHealthcheck(t, finalDockerStage(t))
}

func finalDockerStage(t *testing.T) dockerStage {
	t.Helper()
	stages := parseDockerfile(t, readRepoFile(t, "Dockerfile"))
	return stages[len(stages)-1]
}
