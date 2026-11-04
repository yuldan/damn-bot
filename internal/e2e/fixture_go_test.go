package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// buildGoFixture compiles one test fixture source into a native binary named
// name (".exe" on Windows) and returns the binary plus its temp directory.
func buildGoFixture(t *testing.T, src, name string) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	path := filepath.Join(dir, "fixture.go")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin = filepath.Join(dir, name)
	build := exec.CommandContext(t.Context(), "go", "build", "-o", bin, path) // #nosec G204 -- fixed go tool
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, out)
	}
	return bin, dir
}
