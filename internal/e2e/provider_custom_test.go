package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

const customProviderConfig = `[provider]
model = "kantor/m"

[[providers.custom]]
name = "kantor"
base_url = %q
api = "openai-chat"
api_key_env = "KANTOR_KEY"
models = [{ id = "m", context_length = 32768, max_tokens = 1024, capabilities = ["tools"] }]
`

func writeCustomConfig(t *testing.T, e *Env, baseURL string, extra string) {
	t.Helper()
	body := fmt.Sprintf(customProviderConfig, baseURL) + extra
	if err := os.WriteFile(e.cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestCustomProviderRun proves the binary resolves a custom provider from
// config (no --base-url, no built-in id) through a fake HTTP endpoint.
func TestCustomProviderRun(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("halo dari custom"))
	writeCustomConfig(t, e, e.Server.URL, "")
	res := e.ExecEnv([]string{"KANTOR_KEY=sk-e2e"},
		"run", "-p", "halo", "--model", "kantor/m", "--config", e.cfg,
		"--data-dir", e.DataDir, "--offline", "--output", "json")
	if res.Code != 0 {
		t.Fatalf("run exit = %d\nstdout:\n%s\nstderr:\n%s", res.Code, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "halo dari custom") {
		t.Fatalf("answer missing:\n%s", res.Stdout)
	}
	if bodies := e.Server.Requests(); len(bodies) != 1 || !strings.Contains(string(bodies[0]), `"model":"m"`) {
		t.Fatalf("provider request = %q", bodies)
	}
}

// TestDoctorCustomProviderWarns: doctor --json reports the SEC-24 custom
// findings (unpriced model) as DOC-009 warn.
func TestDoctorCustomProviderWarns(t *testing.T) {
	t.Parallel()
	e := New(t)
	paths := harnessPaths(e)
	writeDoctorConfig(t, paths, fmt.Sprintf(customProviderConfig, "http://127.0.0.1:9/v1"), 0o600)
	res := e.Exec("doctor", "--json")
	var doc struct {
		Checks []struct {
			ID      string `json:"id"`
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(res.Stdout), &doc); err != nil {
		t.Fatalf("doctor --json: %v\nstdout:\n%s\nstderr:\n%s", err, res.Stdout, res.Stderr)
	}
	for _, c := range doc.Checks {
		if c.ID != "DOC-009" {
			continue
		}
		if c.Status != "warn" || !strings.Contains(c.Message, "unpriced") {
			t.Fatalf("DOC-009 = %+v, want warn with unpriced", c)
		}
		return
	}
	t.Fatalf("DOC-009 missing from %+v", doc.Checks)
}
