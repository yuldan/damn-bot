//go:build realprovider && !minimal

package replay

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/registry"
)

// Real-provider smoke (opt-in, never part of the CI replay gate). The test
// binary is loopback-only (E-69), so the smoke builds and runs the real
// cmd/damnbot binary; that binary dials the provider normally.
//
//	DAMNBOT_REAL_PROVIDER=1 go test -tags realprovider ./internal/provider/replay -run TestRealProviders -v
//
// Credentials are read from the catalog env candidates by the child process;
// this harness never prints a value. When DAMNBOT_REAL_PROVIDER_REQUIRED=1
// (the CI job that declares it has keys), a missing gate/credential FAILS
// instead of skipping.

// realProvider is one major provider of the smoke matrix. The model may
// contain a slash: the resolver cuts the reference at the first slash only.
type realProvider struct{ ref, id string }

// realProviders is the >=10 major-provider matrix. Env candidates come from
// the catalog, so a renamed variable is a test failure, not stale data.
func realProviders() []realProvider {
	return []realProvider{
		{"openai/gpt-4o-mini", "openai"},
		{"anthropic/claude-3-5-haiku-latest", "anthropic"},
		{"xai/grok-2-latest", "xai"},
		{"groq/llama-3.3-70b-versatile", "groq"},
		{"mistral/mistral-small-latest", "mistral"},
		{"deepseek/deepseek-chat", "deepseek"},
		{"openrouter/openai/gpt-4o-mini", "openrouter"},
		{"together/meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo", "together"},
		{"fireworks/accounts/fireworks/models/llama-v3p1-8b-instruct", "fireworks"},
		{"perplexity/sonar", "perplexity"},
	}
}

func TestRealProviders(t *testing.T) {
	required := os.Getenv("DAMNBOT_REAL_PROVIDER_REQUIRED") == "1"
	if os.Getenv("DAMNBOT_REAL_PROVIDER") == "" {
		if required {
			t.Fatal("DAMNBOT_REAL_PROVIDER_REQUIRED=1 but DAMNBOT_REAL_PROVIDER is unset")
		}
		t.Skip("set DAMNBOT_REAL_PROVIDER=1 and provider credentials to run the real smoke")
	}
	cat, err := registry.DefaultCatalog()
	if err != nil {
		t.Fatal(err)
	}
	rows := realProviders()
	if len(rows) < 10 {
		t.Fatalf("real smoke matrix = %d providers, want at least 10", len(rows))
	}
	bin := buildSmokeBinary(t)
	for _, row := range rows {
		p, ok := cat.Lookup(row.id)
		if !ok {
			t.Fatalf("%s: not in the provider catalog", row.id)
		}
		if !hasCredential(p.Env) {
			if required {
				t.Errorf("%s: none of %v is set; the CI job declares credentials", row.id, p.Env)
			} else {
				t.Logf("%s: no credential in %v, skipped", row.id, p.Env)
			}
			continue
		}
		t.Run(row.id, func(t *testing.T) { runRealSmoke(t, bin, row.ref) })
	}
}

// hasCredential reports whether one catalog env candidate is set, without
// exposing any value.
func hasCredential(env []string) bool {
	for _, name := range env {
		if os.Getenv(name) != "" {
			return true
		}
	}
	return false
}

func buildSmokeBinary(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("DAMNBOT_REAL_PROVIDER_BIN"); bin != "" {
		return bin
	}
	bin := filepath.Join(t.TempDir(), "damnbot-real-smoke")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-tags", "goolm", "-o", bin, "./cmd/damnbot") // #nosec G204 -- bin is a test temp path
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build smoke binary: %v\n%s", err, out)
	}
	return bin
}

func runRealSmoke(t *testing.T, bin, ref string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), bin, "run", // #nosec G204 -- bin comes from DAMNBOT_REAL_PROVIDER_BIN or the test temp dir
		"-p", "Reply with the single word PONG.",
		"-model", ref,
		"-output", "json",
		"-data-dir", t.TempDir(),
	)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: run failed: %v", ref, err) // stderr is not attached: it may repeat a provider message
	}
	var res struct {
		Answer string `json:"answer"`
	}
	if uerr := json.Unmarshal(out, &res); uerr != nil {
		t.Fatalf("%s: output is not turn JSON: %v", ref, uerr)
	}
	if strings.TrimSpace(res.Answer) == "" {
		t.Fatalf("%s: empty answer", ref)
	}
}

// repoRoot finds the module root from the package directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, serr := os.Stat(filepath.Join(dir, "go.mod")); serr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
