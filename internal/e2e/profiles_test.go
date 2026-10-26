package e2e

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// profileDataDir mirrors config.Paths(profile).DataDir for the harness HOME
// (the child inherits HOME/XDG_CONFIG_HOME/USERPROFILE, not XDG_DATA_HOME).
func profileDataDir(home, profile string) string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(home, "AppData", "Local", "damnbot", "profiles", profile)
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "damnbot", "profiles", profile)
	default:
		return filepath.Join(home, ".local", "share", "damnbot", "profiles", profile)
	}
}

// TestProfileFlagDataDir (SPEC §10 global --profile): the flag is parsed
// before dispatch in both positions and selects <data>/profiles/<name>; the
// default profile is never touched.
func TestProfileFlagDataDir(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("first"), providertest.Text("second"))
	turn := []string{
		"run", "-p", "hello", "--model", modelRef, "--base-url", e.Server.URL,
		"--api-key", "e2e", "--offline", "--output", "json",
	}
	e.Exec(append([]string{"--profile", "work"}, turn...)...).RequireOK(t)
	if _, err := os.Stat(filepath.Join(profileDataDir(e.home, "work"), "damnbot.db")); err != nil {
		t.Fatalf("prefix --profile did not create the profile data dir: %v", err)
	}
	e.Exec(append([]string{"run", "--profile", "after"}, turn[1:]...)...).RequireOK(t)
	if _, err := os.Stat(filepath.Join(profileDataDir(e.home, "after"), "damnbot.db")); err != nil {
		t.Fatalf("post-command --profile did not create the profile data dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(profileDataDir(e.home, "work"), "damnbot.db")); err != nil {
		t.Fatalf("the first profile lost its data dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(profileDataDir(e.home, "after"), "damnbot.db")); err != nil {
		t.Fatalf("the second profile lost its data dir: %v", err)
	}
}

// TestProfileFlagRejectsBadName: an invalid name is a usage error before any
// command runs, and no profile directory is created for it.
func TestProfileFlagRejectsBadName(t *testing.T) {
	t.Parallel()
	e := New(t)
	res := e.Exec("--profile", "Bad Name", "version")
	if res.Code != 2 {
		t.Fatalf("exit = %d, want 2\n%s", res.Code, res.Stderr)
	}
	if res.Stdout != "" {
		t.Errorf("stdout = %q, want empty", res.Stdout)
	}
}
