package e2e

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func runSecret(t *testing.T, e *Env, bin, input string, env []string, args ...string) Result {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), bin, args...) // #nosec G204 -- test-built binary
	cmd.Dir = e.Workdir
	cmd.Env = append(os.Environ(), "HOME="+e.home, "XDG_CONFIG_HOME="+e.home,
		"USERPROFILE="+e.home, "APPDATA="+filepath.Join(e.home, "AppData", "Roaming"),
		"LOCALAPPDATA="+filepath.Join(e.home, "AppData", "Local"))
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = strings.NewReader(input)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := Result{Stdout: out.String(), Stderr: errb.String()}
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		res.Code = exit.ExitCode()
	}
	return res
}

func secretIndex(e *Env) string { return filepath.Join(e.ConfigDir(), "secrets.index") }

func secretFingerprint(t *testing.T, e *Env, name string) string {
	t.Helper()
	b, err := fs.ReadFile(os.DirFS(filepath.Dir(secretIndex(e))), "secrets.index")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if n, fp, ok := strings.Cut(line, "\t"); ok && n == name {
			return fp
		}
	}
	return ""
}

func TestSecretsSetListRotate(t *testing.T) {
	t.Parallel()
	e := New(t)
	bin := binPaths.secret
	set := runSecret(t, e, bin, "s3cr3t-value\n", nil, "setup", "secrets", "set", "openrouter")
	set.RequireOK(t)
	if strings.Contains(set.Stdout, "s3cr3t-value") || strings.Contains(set.Stderr, "s3cr3t-value") {
		t.Fatal("set printed the secret value")
	}
	refs := runSecret(t, e, bin, "", nil, "setup", "secrets", "ls", "--refs")
	refs.RequireOK(t)
	if refs.Stdout != "openrouter\n" {
		t.Fatalf("ls --refs = %q", refs.Stdout)
	}
	index, err := fs.ReadFile(os.DirFS(filepath.Dir(secretIndex(e))), "secrets.index")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(index), "s3cr3t-value") {
		t.Fatal("index contains the secret value")
	}
	fi, err := os.Stat(secretIndex(e))
	if err != nil || (fi.Mode().Perm() != 0o600 && runtime.GOOS != "windows") {
		t.Fatalf("index mode = %v, %v", fi.Mode().Perm(), err)
	}
	before := secretFingerprint(t, e, "openrouter")
	if before == "" {
		t.Fatal("missing fingerprint")
	}
	rot := runSecret(t, e, bin, "new-secret-value\n", nil, "setup", "secrets", "rotate", "openrouter")
	rot.RequireOK(t)
	if after := secretFingerprint(t, e, "openrouter"); after == "" || after == before {
		t.Fatalf("fingerprint did not change: %q -> %q", before, after)
	}
	dup := runSecret(t, e, bin, "again\n", nil, "setup", "secrets", "set", "openrouter")
	if dup.Code == 0 || !strings.Contains(dup.Stderr, "use rotate") {
		t.Fatalf("duplicate set = (%d, %s)", dup.Code, dup.Stderr)
	}
}

func TestSecretsKeyringUnavailable(t *testing.T) {
	t.Parallel()
	e := New(t)
	bin := binPaths.secret
	res := runSecret(t, e, bin, "value\n", []string{"DAMNBOT_TEST_KEYRING=fail"}, "setup", "secrets", "set", "token")
	if res.Code == 0 || !strings.Contains(res.Stderr, "keychain") {
		t.Fatalf("no-keyring set = (%d, %s)", res.Code, res.Stderr)
	}
	if _, err := os.Stat(secretIndex(e)); !os.IsNotExist(err) {
		t.Fatal("index written while keyring is unavailable")
	}
}

func TestSecretsCLIUsage(t *testing.T) {
	t.Parallel()
	e := New(t)
	bin := binPaths.secret
	for _, args := range [][]string{
		{"setup", "secrets", "set"},
		{"setup", "secrets", "set", "Bad Name"},
		{"setup", "secrets", "nope"},
	} {
		res := runSecret(t, e, bin, "", nil, args...)
		if res.Code != 2 {
			t.Fatalf("%v exit = %d, want 2", args, res.Code)
		}
	}
	res := runSecret(t, e, bin, "", nil, "setup", "secrets", "rotate", "--all-profiles", "token")
	if res.Code != 2 || !strings.Contains(res.Stderr, "not implemented") {
		t.Fatalf("rotate --all-profiles = (%d, %s), want explicit unsupported", res.Code, res.Stderr)
	}
}
