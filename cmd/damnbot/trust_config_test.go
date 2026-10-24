package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// trustedRunConfig writes a config.toml that declares dir a trusted
// workspace. An in-process run uses the same production mechanism as a user
// (`skills.trusted_projects`, the config half of the trust store), because
// headless `run` is untrusted by default (SPEC §14): a test whose workspace is
// the checkout must say so instead of relying on the old hard-coded default.
// It returns the path for `--config`.
func trustedRunConfig(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.toml")
	body := "[skills]\ntrusted_projects = [" + strconv.Quote(dir) + "]\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// cwdTrustedRunConfig is trustedRunConfig for the current working directory
// (after any t.Chdir the caller performed).
func cwdTrustedRunConfig(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return trustedRunConfig(t, cwd)
}
