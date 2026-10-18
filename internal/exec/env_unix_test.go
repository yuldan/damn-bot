//go:build unix

package exec

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runEnv runs o (mode a when Direct is nil) and returns the child's
// environment as printed by env(1).
func runEnv(t *testing.T, o *OneShot) (map[string]string, string) {
	t.Helper()
	spec, notice, err := Prepare(o)
	if err != nil {
		t.Fatal(err)
	}
	res, err := newTestRunner(t).Run(t.Context(), &spec)
	if err != nil || res.Status != StatusExited {
		t.Fatalf("%v %s %q", err, res.Status, res.Output)
	}
	got := map[string]string{}
	for line := range strings.SplitSeq(strings.ReplaceAll(string(res.Output), "\r", ""), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok {
			got[k] = v
		}
	}
	return got, notice
}

// TestEnvAllowlist (GAP-SEC-02, ADR-071; F1 subset): 50 secret-like and 20
// innocuous parent variables never reach the child, pipe and PTY.
func TestEnvAllowlist(t *testing.T) {
	t.Parallel()
	rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // G404: deterministic test data, not secrets
	parent := []string{
		"PATH=" + os.Getenv("PATH"), "HOME=/home/u", "LANG=C.UTF-8", "LC_ALL=C", "XDG_CONFIG_HOME=/x",
		"CARGO_HOME=/c", "GH_AUTH_HOME=/leak", "TERM=xterm-kitty", "PAGER=less", "GIT_TERMINAL_PROMPT=1",
		"HTTPS_PROXY=http://user:pw@proxy:3128", "NO_PROXY=localhost", "SSH_AUTH_SOCK=/tmp/agent",
		"DAMNBOT_EXEC_ID=forged", "OPENAI_API_KEY=sk-allowed-by-name",
	}
	var leaks []string
	for i := range 50 {
		name := fmt.Sprintf("%s_%d_%s", []string{"STRIPE", "AWS", "GH", "SVC", "DB"}[rng.IntN(5)], i,
			[]string{"KEY", "TOKEN", "SECRET", "ACCESS_KEY_ID", "PASSWORD"}[rng.IntN(5)])
		leaks = append(leaks, name)
	}
	for i := range 20 {
		leaks = append(leaks, []string{"FOO_CREDS", "DATABASE_URL", "BAR", "MY_CONFIG", "SENTRY_DSN"}[i%5]+fmt.Sprint(i))
	}
	for _, k := range leaks {
		parent = append(parent, k+"=v-"+k)
	}
	policy := &EnvPolicy{Allow: []string{"OPENAI_API_KEY"}, Set: map[string]string{"PAGER": "more", "GIT_TERMINAL_PROMPT": "1", "X_SET": "1"}}
	for _, pty := range []bool{false, true} {
		got, _ := runEnv(t, &OneShot{Direct: []string{"env"}, Dir: t.TempDir(), PTY: pty, Parent: parent, Policy: policy})
		for _, k := range append(leaks, "GH_AUTH_HOME", "SSH_AUTH_SOCK") {
			if _, ok := got[k]; ok {
				t.Errorf("pty=%v: %s leaked", pty, k)
			}
		}
		want := map[string]string{
			"HOME": "/home/u", "LANG": "C.UTF-8", "LC_ALL": "C", "XDG_CONFIG_HOME": "/x", "CARGO_HOME": "/c",
			"OPENAI_API_KEY": "sk-allowed-by-name", "PAGER": "more", "X_SET": "1", "GIT_TERMINAL_PROMPT": "0",
			"HTTPS_PROXY": "http://proxy:3128", "NO_PROXY": "localhost", "EDITOR": "false", "CI": "1",
			"GIT_SSH_COMMAND": "ssh -o BatchMode=yes", "DAMNBOT": "1",
		}
		want["TERM"] = map[bool]string{false: "dumb", true: "xterm-256color"}[pty]
		for k, v := range want {
			if got[k] != v {
				t.Errorf("pty=%v: %s=%q, want %q", pty, k, got[k], v)
			}
		}
		if got[MarkerEnv] == "forged" || got[MarkerEnv] == "" {
			t.Errorf("marker %q", got[MarkerEnv])
		}
	}
	got, _ := runEnv(t, &OneShot{Direct: []string{"env"}, Dir: t.TempDir(), Parent: parent, Policy: &EnvPolicy{PassSSHAgent: true}})
	if got["SSH_AUTH_SOCK"] != "/tmp/agent" || got["OPENAI_API_KEY"] != "" {
		t.Errorf("pass_ssh_agent: %v", got)
	}
}

// TestSecretRefScope (ADR-081, ADR-088 butir 4, NEW3-03): a secret ref
// reaches only a direct-exec child whose argv0 is in its commands.
func TestSecretRefScope(t *testing.T) {
	t.Parallel()
	policy := &EnvPolicy{Secrets: []SecretEnv{{Name: "GH_TOKEN", Value: "ghp-secret", Commands: []string{"env"}}}}
	parent := []string{"PATH=" + os.Getenv("PATH")}
	got, notice := runEnv(t, &OneShot{Direct: []string{"env"}, Dir: t.TempDir(), Parent: parent, Policy: policy})
	if got["GH_TOKEN"] != "ghp-secret" || notice != "" {
		t.Errorf("mode b, argv0 in commands: %q %q", got["GH_TOKEN"], notice)
	}
	envBin, err := lookPath("env", os.Getenv("PATH"), "")
	if err != nil {
		t.Skip(err)
	}
	got, _ = runEnv(t, &OneShot{Direct: []string{envBin}, Dir: t.TempDir(), Parent: parent, Policy: policy})
	if got["GH_TOKEN"] != "ghp-secret" {
		t.Errorf("mode b, absolute argv0 with the same name: %q", got["GH_TOKEN"])
	}
	got, notice = runEnv(t, &OneShot{Command: "env", Dir: t.TempDir(), Parent: parent, Policy: policy})
	if _, ok := got["GH_TOKEN"]; ok || notice != "" {
		t.Errorf("mode a with the same argv0: leaked=%v notice=%q", ok, notice)
	}
	got, notice = runEnv(t, &OneShot{Command: `env; echo "t=${GH_TOKEN}"`, Dir: t.TempDir(), Parent: parent, Policy: policy})
	if _, ok := got["GH_TOKEN"]; ok || got["t"] != "" || !strings.Contains(notice, "GH_TOKEN") {
		t.Errorf("NEW3-03 expansion: leaked=%v t=%q notice=%q", ok, got["t"], notice)
	}
	got, _ = runEnv(t, &OneShot{Direct: []string{"printenv"}, Dir: t.TempDir(), Parent: parent, Policy: policy})
	if len(got) == 0 || got["GH_TOKEN"] != "" {
		t.Errorf("mode b, other argv0: %v", got["GH_TOKEN"])
	}
}

// TestLookPath (NEW3-04): relative and empty PATH entries never resolve;
// names with a separator are taken relative to the working directory.
func TestLookPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "tool"), []byte("#!/bin/sh\n"), 0o700); err != nil { //nolint:gosec // G306: must be executable
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "noexec"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"bin", "./bin", "", ":bin:"} {
		if p, err := lookPath("tool", path, root); err == nil {
			t.Errorf("PATH %q resolved %s", path, p)
		}
	}
	if p, err := lookPath("tool", "/nonexistent:"+bin, root); err != nil || p != filepath.Join(bin, "tool") {
		t.Errorf("absolute entry: %q %v", p, err)
	}
	if _, err := lookPath("noexec", bin, root); err == nil {
		t.Error("non-executable resolved")
	}
	if p, err := lookPath("bin/tool", "", root); err != nil || p != "bin/tool" {
		t.Errorf("relative name: %q %v", p, err)
	}
	if _, _, err := Prepare(&OneShot{Command: "true", Parent: []string{"PATH=relative"}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("no shell on a relative-only PATH: %v", err)
	}
}
