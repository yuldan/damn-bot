package archtest

import (
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// dockerInstruction is one Dockerfile instruction after line continuations are
// joined (`\` at end of line).
type dockerInstruction struct {
	Key string   // upper-case verb, e.g. RUN
	Raw string   // full instruction text
	Arg []string // whitespace fields after the verb
}

// dockerStage is one FROM block. Build-time ARGs before the first FROM are
// global and not part of a stage.
type dockerStage struct {
	Base         string
	Name         string
	Instructions []dockerInstruction
}

// parseDockerfile is deliberately small: tests read the real Dockerfile, so a
// syntax error fails here instead of only in CI's docker build.
func parseDockerfile(t *testing.T, text string) []dockerStage {
	t.Helper()
	var stages []dockerStage
	var pending string
	for raw := range strings.SplitSeq(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pending = joinDockerLine(pending, strings.TrimSuffix(line, "\\"))
		if !strings.HasSuffix(strings.TrimSpace(raw), "\\") {
			stages = appendDockerInstruction(stages, pending)
			pending = ""
		}
	}
	stages = appendDockerInstruction(stages, pending)
	if len(stages) == 0 {
		t.Fatal("Dockerfile: tidak ada instruksi FROM")
	}
	return stages
}

func joinDockerLine(pending, line string) string {
	if pending == "" {
		return line
	}
	return pending + " " + line
}

func appendDockerInstruction(stages []dockerStage, text string) []dockerStage {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return stages
	}
	ins := dockerInstruction{Key: strings.ToUpper(fields[0]), Raw: text, Arg: fields[1:]}
	if ins.Key == "FROM" {
		return append(stages, newDockerStage(ins))
	}
	if len(stages) == 0 {
		return stages // ARG before the first FROM
	}
	stages[len(stages)-1].Instructions = append(stages[len(stages)-1].Instructions, ins)
	return stages
}

func newDockerStage(ins dockerInstruction) dockerStage {
	st := dockerStage{Instructions: []dockerInstruction{ins}}
	for i, arg := range ins.Arg {
		if strings.HasPrefix(arg, "--") {
			continue
		}
		if strings.EqualFold(arg, "AS") && i+1 < len(ins.Arg) {
			st.Name = ins.Arg[i+1]
			break
		}
		if st.Base == "" {
			st.Base = arg
		}
	}
	return st
}

// stageInstruction returns the first instruction with key in st, or nil.
func stageInstruction(st dockerStage, key string) *dockerInstruction {
	for i := range st.Instructions {
		if st.Instructions[i].Key == key {
			return &st.Instructions[i]
		}
	}
	return nil
}

// TestDockerfileDistroless (GAP-INF-03, GAP-SEC-22, ADR-033 Q35/Q49) parses
// the shipped Dockerfile: the runtime stage is distroless (FROM scratch or a
// distroless base) and contains no RUN/ADD, so the image carries no shell and
// no compiler; the binary is built with CGO_ENABLED=0 -tags goolm for
// TARGETOS/TARGETARCH, runs as 65532:65532, and declares the REL-17 healthcheck.
func TestDockerfileDistroless(t *testing.T) {
	t.Parallel()
	stages := parseDockerfile(t, readRepoFile(t, "Dockerfile"))
	checkDockerRuntimeStage(t, stages[len(stages)-1])
	checkDockerBuildStage(t, stages[0])

	ignore := readRepoFile(t, ".dockerignore")
	for _, want := range []string{".git", "f4-gwstore", "**/*_test.go"} {
		if !strings.Contains(ignore, want) {
			t.Errorf(".dockerignore tidak mengecualikan %q", want)
		}
	}
}

func checkDockerRuntimeStage(t *testing.T, final dockerStage) {
	t.Helper()
	checkDockerRuntimeBase(t, final)
	checkDockerNoExecutor(t, final)
	checkDockerIdentity(t, final)
}

func checkDockerRuntimeBase(t *testing.T, final dockerStage) {
	t.Helper()
	if final.Base != "scratch" && !strings.Contains(final.Base, "distroless") {
		t.Fatalf("runtime stage base = %q, want scratch atau image distroless (tanpa shell/compiler)", final.Base)
	}
	if final.Base == "scratch" && stageInstruction(final, "COPY") == nil {
		t.Fatal("runtime stage scratch tidak menyalin binary")
	}
}

func checkDockerNoExecutor(t *testing.T, final dockerStage) {
	t.Helper()
	for _, ins := range final.Instructions {
		switch ins.Key {
		case "RUN":
			t.Fatalf("runtime stage menjalankan RUN (butuh shell): %s", ins.Raw)
		case "ADD":
			t.Fatalf("runtime stage memakai ADD (dapat mengunduh/mengekstrak): %s", ins.Raw)
		}
	}
}

func checkDockerIdentity(t *testing.T, final dockerStage) {
	t.Helper()
	user := stageInstruction(final, "USER")
	if user == nil || len(user.Arg) != 1 || user.Arg[0] != "65532:65532" {
		t.Fatalf("USER runtime = %v, want 65532:65532 (distroless nonroot)", user)
	}
	checkDockerHealthcheck(t, final)
	entry := stageInstruction(final, "ENTRYPOINT")
	if entry == nil || !strings.Contains(entry.Raw, `["/damnbot"]`) {
		t.Fatalf("ENTRYPOINT = %v, want [\"/damnbot\"]", entry)
	}
}

func checkDockerHealthcheck(t *testing.T, final dockerStage) {
	t.Helper()
	health := stageInstruction(final, "HEALTHCHECK")
	if health == nil || !strings.Contains(health.Raw, `["/damnbot"`) ||
		!strings.Contains(health.Raw, `"doctor"`) || !strings.Contains(health.Raw, `"health"`) ||
		!strings.Contains(health.Raw, `"--ready"`) {
		t.Fatalf("HEALTHCHECK = %v, want exec-form /damnbot doctor health --ready (REL-17)", health)
	}
}

func checkDockerBuildStage(t *testing.T, build dockerStage) {
	t.Helper()
	var text strings.Builder
	for _, ins := range build.Instructions {
		text.WriteString(ins.Raw)
		text.WriteByte('\n')
	}
	for _, want := range []string{
		"ARG TARGETOS", "ARG TARGETARCH",
		"CGO_ENABLED=0", "GOOS=$TARGETOS", "GOARCH=$TARGETARCH",
		"-tags goolm", "-o /out/damnbot ./cmd/damnbot",
	} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("build stage tidak memuat %q (multi-arch static build)", want)
		}
	}
}

// TestDockerNoSocketMount (GAP-SEC-22) parses docker-compose.yml: the official
// compose file never mounts the host docker socket, runs non-root with a
// read-only root filesystem, drops capabilities and keeps the default bridge
// network (no network_mode/privileged escape hatch). Access to docker.sock is
// only the documented opt-in outside this file.
func TestDockerNoSocketMount(t *testing.T) {
	t.Parallel()
	checkCompose(t, readRepoFile(t, "docker-compose.yml"))
}

func checkCompose(t *testing.T, raw string) {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("docker-compose.yml tidak valid: %v", err)
	}
	lower := strings.ToLower(raw)
	if strings.Contains(lower, "docker.sock") || strings.Contains(lower, "/var/run/docker") {
		t.Fatal("docker-compose.yml memasang docker sock; akses hanya opt-in DAMNBOT_ALLOW_DOCKER_SOCKET=1")
	}
	services, ok := doc["services"].(map[string]any)
	if !ok {
		t.Fatal("docker-compose.yml: services bukan map")
	}
	svc, ok := services["damnbot"].(map[string]any)
	if !ok {
		t.Fatal("docker-compose.yml: service damnbot tidak ada")
	}
	checkComposeService(t, svc)
}

// checkComposeService asserts the non-root, read-only, capability-free shape of
// the official service.
func checkComposeService(t *testing.T, svc map[string]any) {
	t.Helper()
	if got, isString := svc["user"].(string); !isString || got != "65532:65532" {
		t.Errorf("compose user = %v, want 65532:65532", svc["user"])
	}
	if got, isBool := svc["read_only"].(bool); !isBool || !got {
		t.Error("compose read_only != true")
	}
	if got, isBool := svc["privileged"].(bool); isBool && got {
		t.Error("compose privileged = true")
	}
	if got, isString := svc["network_mode"].(string); isString && got != "" {
		t.Errorf("compose network_mode = %q, want jaringan default", got)
	}
	if !containsString(svc["cap_drop"], "ALL") {
		t.Errorf("compose cap_drop = %v, want ALL", svc["cap_drop"])
	}
	if !containsString(svc["security_opt"], "no-new-privileges:true") {
		t.Errorf("compose security_opt = %v, want no-new-privileges:true", svc["security_opt"])
	}
}

// containsString reports whether a YAML list value contains want.
func containsString(v any, want string) bool {
	list, ok := v.([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		if s, isString := item.(string); isString && s == want {
			return true
		}
	}
	return false
}
