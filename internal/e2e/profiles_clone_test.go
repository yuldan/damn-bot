package e2e

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProfileCloneNoSecretValues: the F3 profile-copy path (export then
// import, the only profile copy surface in SPEC §10) must carry references,
// never values. The source profile has an at-rest secret store and a session
// DB containing the sentinel; neither may appear in the clone.
func TestProfileCloneNoSecretValues(t *testing.T) {
	t.Parallel()
	e := New(t)
	const sentinel = "SENTINEL-SECRET-123"
	srcDir := filepath.Join(e.ConfigDir(), "profiles", "source")
	writeNamedProfileConfig(t, e, "source", `[provider]
model = "openai/gpt-fake"

[approval]
shell_allow = [["go", "test", "./..."]]

[[providers.custom]]
name = "kantor"
base_url = "https://example.invalid/v1"
api_key = "keychain:openrouter"
models = [{ id = "qwen3-coder", context_length = 131072 }]
`)
	for _, name := range []string{"secrets.enc", "damnbot.db"} {
		if err := os.WriteFile(filepath.Join(srcDir, name), []byte(sentinel), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e.Exec("config", "profiles", "export", "source", "-o", "source.tar.gz").RequireOK(t)
	e.Exec("config", "profiles", "import", "source.tar.gz", "--name", "clone").RequireOK(t)
	cloneDir := filepath.Join(e.ConfigDir(), "profiles", "clone")
	cfg := string(readProfileConfig(t, cloneDir))
	for _, want := range []string{
		`model = "openai/gpt-fake"`,
		`base_url = "https://example.invalid/v1"`,
		`api_key = "keychain:openrouter"`,
		`shell_allow = [["go", "test", "./..."]]`,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("cloned config lacks %q:\n%s", want, cfg)
		}
	}
	e.Exec("--profile", "clone", "config", "show").RequireOK(t)
	if path, ok := profileTreeContains(t, cloneDir, sentinel); ok {
		t.Fatalf("clone copied secret value into %s", path)
	}

	// Negative control: a clone that copies the secret store would be caught
	// by the same detector.
	leak := filepath.Join(cloneDir, "secrets.enc")
	if err := os.WriteFile(leak, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	if path, ok := profileTreeContains(t, cloneDir, sentinel); !ok {
		t.Fatal("detector missed a sentinel copied into the clone")
	} else if path != "secrets.enc" {
		t.Fatalf("detector path = %q", path)
	}
	if err := os.Remove(leak); err != nil {
		t.Fatal(err)
	}
}

// profileTreeContains returns the first regular file under root containing
// needle. Reads go through os.DirFS with a slash-relative name.
func profileTreeContains(t *testing.T, root, needle string) (string, bool) {
	t.Helper()
	var found string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("rel %s: %w", path, err)
		}
		b, err := fs.ReadFile(os.DirFS(root), filepath.ToSlash(rel))
		if err != nil {
			return fmt.Errorf("read %s: %w", rel, err)
		}
		if bytes.Contains(b, []byte(needle)) {
			found = filepath.ToSlash(rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found, found != ""
}
