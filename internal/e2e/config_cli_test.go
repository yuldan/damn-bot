package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProfileConfig writes the child's profile config.toml (the path the
// binary derives from HOME/XDG, not the harness's explicit --config file).
func writeProfileConfig(t *testing.T, e *Env, content string) string {
	t.Helper()
	dir := e.ConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

func configHash(t *testing.T, path string) string {
	t.Helper()
	b, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestConfigShow: `damnbot config show` exits 0 and its effective TOML is
// byte-stable across runs.
func TestConfigShow(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeProfileConfig(t, e, "[provider]\nmodel = \"openai/gpt-fake\"\n")
	first := e.Exec("config", "show")
	first.RequireOK(t)
	for _, want := range []string{"[provider]", `model = "openai/gpt-fake"`, "[loop]", "max_iterations = 40"} {
		if !strings.Contains(first.Stdout, want) {
			t.Errorf("show output lacks %q:\n%s", want, first.Stdout)
		}
	}
	second := e.Exec("config", "show")
	second.RequireOK(t)
	if first.Stdout != second.Stdout {
		t.Error("config show is not stable between runs")
	}
}

// TestConfigGetUnknownKey: an unknown key is a usage error with a clear
// message and no stdout.
func TestConfigGetUnknownKey(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeProfileConfig(t, e, "[provider]\nmodel = \"openai/gpt-fake\"\n")
	res := e.Exec("config", "get", "provider.nope")
	if res.Code == 0 {
		t.Fatalf("exit = 0, want non-zero\n%s", res.Stdout)
	}
	if res.Stdout != "" {
		t.Errorf("stdout = %q, want empty", res.Stdout)
	}
	if !strings.Contains(res.Stderr, `unknown key "provider.nope"`) {
		t.Errorf("stderr lacks the unknown-key message: %s", res.Stderr)
	}
}

// TestConfigEditDoesNotWrite: --dry-run and a non-terminal invocation only
// validate; the config bytes are unchanged.
func TestConfigEditDoesNotWrite(t *testing.T) {
	t.Parallel()
	e := New(t)
	file := writeProfileConfig(t, e, "[provider]\nmodel = \"openai/gpt-fake\"\n")
	before := configHash(t, file)

	dry := e.Exec("config", "edit", "--dry-run")
	dry.RequireOK(t)
	if !strings.Contains(dry.Stdout, "not written (dry run)") {
		t.Errorf("dry-run stdout = %q", dry.Stdout)
	}
	if after := configHash(t, file); after != before {
		t.Error("edit --dry-run modified config.toml")
	}

	plain := e.Exec("config", "edit")
	plain.RequireOK(t)
	if !strings.Contains(plain.Stdout, "not written (no terminal)") {
		t.Errorf("non-terminal stdout = %q", plain.Stdout)
	}
	if after := configHash(t, file); after != before {
		t.Error("edit without a terminal modified config.toml")
	}
}
