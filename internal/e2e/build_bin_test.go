package e2e

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// buildE2EBin builds one tagged child binary under the temp dir, appending
// .exe on Windows so the exec path matches what `go build -o` writes.
func buildE2EBin(t *testing.T, tags, name string) string {
	t.Helper()
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	out := filepath.Join(t.TempDir(), name)
	build := exec.CommandContext(t.Context(), "go", "build", "-tags", tags, "-o", out, "./cmd/damnbot") // #nosec G204 -- fixed go tool
	build.Dir = root
	if b, berr := build.CombinedOutput(); berr != nil {
		t.Fatalf("build %s: %v\n%s", name, berr, b)
	}
	return out
}
