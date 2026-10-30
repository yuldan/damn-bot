package archtest

import (
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
)

// TestEnvAllowlist (GAP-OPS-01): os.Getenv/os.LookupEnv live only in
// internal/config; every other package uses the schema override or Deps.
func TestEnvAllowlist(t *testing.T) {
	root := os.DirFS("../..")
	for _, dir := range []string{"cmd", "internal"} {
		err := fs.WalkDir(root, dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			return checkEnvFile(t, root, path)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func checkEnvFile(t *testing.T, root fs.FS, path string) error {
	t.Helper()
	if strings.Contains(path, "internal/config") || strings.Contains(path, "internal/testutil/") {
		return nil
	}
	if strings.HasSuffix(path, "internal/codetools/codetools.go") || strings.HasSuffix(path, "internal/secret/backend_linux.go") {
		return nil
	}
	body, err := fs.ReadFile(root, path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if strings.Contains(string(body), "os.Getenv(") || strings.Contains(string(body), "os.LookupEnv(") {
		t.Errorf("%s calls os.Getenv/os.LookupEnv outside internal/config", path)
	}
	return nil
}
