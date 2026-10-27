package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
)

// configTestHome isolates HOME/XDG/APPDATA and writes one profile config.
func configTestHome(t *testing.T, content string) (file, cfgDir string) {
	t.Helper()
	home := t.TempDir()
	for _, kv := range [][2]string{
		{"HOME", home},
		{"USERPROFILE", home},
		{"XDG_CONFIG_HOME", home},
		{"XDG_DATA_HOME", home},
		{"APPDATA", filepath.Join(home, "AppData", "Roaming")},
		{"LOCALAPPDATA", filepath.Join(home, "AppData", "Local")},
	} {
		t.Setenv(kv[0], kv[1])
	}
	setActiveProfile("")
	t.Cleanup(func() { setActiveProfile("") })
	loader, err := config.New(config.OS())
	if err != nil {
		t.Fatal(err)
	}
	paths, err := loader.Paths("")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	file = paths.ConfigFile()
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return file, paths.ConfigDir
}

func configTestHash(t *testing.T, path string) string {
	t.Helper()
	b, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

const configTestSpec = `[provider]
model = "openai/gpt-test"

[exec]
env = { TOKEN = "supersecret" }

[[providers.custom]]
name = "kantor"
base_url = "http://10.0.0.5:8000/v1"
api_key = "keychain:kantor"
models = [{ id = "qwen3-coder", context_length = 131072 }]
extra_headers = { Authorization = "Bearer topsecret" }
`

func TestConfigShowMasksSecrets(t *testing.T) {
	configTestHome(t, configTestSpec)
	code, out, errOut := runArgs(t, "config", "show")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
	for _, secret := range []string{"supersecret", "keychain:kantor", "Bearer topsecret"} {
		if strings.Contains(out, secret) {
			t.Errorf("show leaked %q:\n%s", secret, out)
		}
	}
	for _, want := range []string{"[loop]", "max_iterations = 40", `message_content_ttl = "24h0m0s"`, `"***"`} {
		if !strings.Contains(out, want) {
			t.Errorf("show output lacks %q:\n%s", want, out)
		}
	}
}

func TestConfigShowStable(t *testing.T) {
	configTestHome(t, configTestSpec)
	_, first, _ := runArgs(t, "config", "show")
	_, second, _ := runArgs(t, "config", "show")
	if first != second {
		t.Error("show output changed between identical runs")
	}
}

func TestConfigGet(t *testing.T) {
	configTestHome(t, configTestSpec)
	if code, out, errOut := runArgs(t, "config", "get", "provider.model"); code != 0 || out != "\"openai/gpt-test\"\n" {
		t.Fatalf("get provider.model = (%d, %q, %s)", code, out, errOut)
	}
	if code, out, _ := runArgs(t, "config", "get", "exec.env.TOKEN"); code != 0 || out != "\"***\"\n" {
		t.Fatalf("get secret = (%d, %q), want masked", code, out)
	}
}

func TestConfigGetUnknownKey(t *testing.T) {
	configTestHome(t, configTestSpec)
	code, out, errOut := runArgs(t, "config", "get", "provider.nope")
	if code != 2 || out != "" {
		t.Fatalf("exit = %d, stdout = %q, want usage error", code, out)
	}
	if !strings.Contains(errOut, `unknown key "provider.nope"`) {
		t.Errorf("stderr lacks the unknown-key message: %s", errOut)
	}
}

func TestConfigExplain(t *testing.T) {
	file, _ := configTestHome(t, configTestSpec)
	code, out, errOut := runArgs(t, "config", "explain", "provider.model")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, errOut)
	}
	for _, want := range []string{`provider.model = "openai/gpt-test"`, "source = file", "file = " + file + ":2"} {
		if !strings.Contains(out, want) {
			t.Errorf("explain output lacks %q:\n%s", want, out)
		}
	}
	code, out, _ = runArgs(t, "config", "explain", "loop.max_iterations")
	if code != 0 || !strings.Contains(out, "source = default") {
		t.Fatalf("default explain = (%d, %q), want source = default", code, out)
	}
}

func TestConfigSchema(t *testing.T) {
	configTestHome(t, configTestSpec)
	code, out, errOut := runArgs(t, "config", "schema", "exec")
	if code != 0 {
		t.Fatalf("schema exec = (%d, %s)", code, errOut)
	}
	for _, want := range []string{"[exec]", "env_allow", "[exec.env]", "secret: exec.env.<name>"} {
		if !strings.Contains(out, want) {
			t.Errorf("schema exec lacks %q:\n%s", want, out)
		}
	}
	code, out, _ = runArgs(t, "config", "schema", "providers")
	if code != 0 || !strings.Contains(out, "data-only") || !strings.Contains(out, "[[providers.custom]]") || !strings.Contains(out, "key_cmd") {
		t.Fatalf("schema providers = (%d, %q)", code, out)
	}
	if code, _, _ := runArgs(t, "config", "schema", "nope"); code != 2 {
		t.Errorf("schema nope: exit = %d, want 2", code)
	}
}

func TestConfigEditWithoutTerminalDoesNotWrite(t *testing.T) {
	file, _ := configTestHome(t, configTestSpec)
	before := configTestHash(t, file)
	code, out, errOut := runArgs(t, "config", "edit", "--dry-run")
	if code != 0 || !strings.Contains(out, "not written (dry run)") {
		t.Fatalf("edit --dry-run = (%d, %q, %s)", code, out, errOut)
	}
	if after := configTestHash(t, file); after != before {
		t.Error("edit --dry-run modified the config file")
	}
	code, out, errOut = runArgs(t, "config", "edit")
	if code != 0 || !strings.Contains(out, "not written (no terminal)") {
		t.Fatalf("edit without tty = (%d, %q, %s)", code, out, errOut)
	}
	if after := configTestHash(t, file); after != before {
		t.Error("edit without a terminal modified the config file")
	}
}

func TestConfigEditRejectsInvalidConfig(t *testing.T) {
	file, _ := configTestHome(t, "[loop]\nmax_iterations = 99\n")
	before := configTestHash(t, file)
	if code, _, errOut := runArgs(t, "config", "edit", "--dry-run"); code != 1 || !strings.Contains(errOut, "max_iterations") {
		t.Fatalf("invalid dry-run = (%d, %s), want exit 1 naming the key", code, errOut)
	}
	if after := configTestHash(t, file); after != before {
		t.Error("invalid dry-run modified the config file")
	}
}

func TestConfigProfilesLs(t *testing.T) {
	_, cfgDir := configTestHome(t, configTestSpec)
	for _, name := range []string{"work", "beta"} {
		if err := os.MkdirAll(filepath.Join(cfgDir, "profiles", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	code, out, errOut := runArgs(t, "config", "profiles", "ls")
	if code != 0 {
		t.Fatalf("profiles ls = (%d, %s)", code, errOut)
	}
	want := "default\nbeta\nwork\n"
	if out != want {
		t.Fatalf("profiles ls = %q, want %q", out, want)
	}
}
