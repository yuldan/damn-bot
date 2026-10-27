//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/exec/classify"
)

// TestDialectUsesChildEnvPath (SEC10-31): exec.env PATH can add a pwsh
// directory absent from the process PATH; the classifier must use the child
// environment's choice (Pwsh), not os.Environ().
func TestDialectUsesChildEnvPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pwsh.exe"), []byte("stub"), 0o700); err != nil {
		t.Fatal(err)
	}
	policy := &exec.EnvPolicy{Set: map[string]string{"PATH": dir}}
	env := policy.Env([]string{"PATH=" + os.Getenv("PATH")}, false, "", "", "")
	if got := dialectFromEnv(env); got != classify.Pwsh {
		t.Fatalf("dialectFromEnv = %v, want Pwsh (env PATH %q)", got, dir)
	}
}
