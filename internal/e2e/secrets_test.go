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

func openrouterFingerprint(t *testing.T, e *Env, profile string) string {
	t.Helper()
	dir := e.ConfigDir()
	if profile != "" {
		dir = filepath.Join(dir, "profiles", profile)
	}
	b, err := fs.ReadFile(os.DirFS(dir), "secrets.index")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if n, fp, ok := strings.Cut(line, "\t"); ok && n == "openrouter" {
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
	if !strings.HasPrefix(refs.Stdout, "openrouter\tdefault\t") || strings.Count(refs.Stdout, "\n") != 1 {
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
	before := openrouterFingerprint(t, e, "")
	if before == "" {
		t.Fatal("missing fingerprint")
	}
	rot := runSecret(t, e, bin, "new-secret-value\n", nil, "setup", "secrets", "rotate", "openrouter")
	rot.RequireOK(t)
	if after := openrouterFingerprint(t, e, ""); after == "" || after == before {
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

func TestSecretsRotateAllProfiles(t *testing.T) {
	t.Parallel()
	e := New(t)
	bin := binPaths.secret
	for _, profile := range []string{"p1", "p2"} {
		set := runSecret(t, e, bin, "old-"+profile+"\n", nil, "--profile", profile, "setup", "secrets", "set", "openrouter")
		set.RequireOK(t)
	}
	if err := os.MkdirAll(filepath.Join(e.ConfigDir(), "profiles", "p3"), 0o700); err != nil {
		t.Fatal(err)
	}
	before1 := openrouterFingerprint(t, e, "p1")
	before2 := openrouterFingerprint(t, e, "p2")
	if before1 == "" || before2 == "" || before1 == before2 {
		t.Fatalf("fingerprints = %q, %q", before1, before2)
	}
	res := runSecret(t, e, bin, "brand-new-value\n", nil, "setup", "secrets", "rotate", "--all-profiles", "openrouter")
	res.RequireOK(t)
	after1 := openrouterFingerprint(t, e, "p1")
	after2 := openrouterFingerprint(t, e, "p2")
	requireRotateAllOutput(t, res, before1, after1, before2, after2)
	failed := runSecret(t, e, bin, "another-value\n", []string{"DAMNBOT_TEST_KEYRING=fail"},
		"setup", "secrets", "rotate", "--all-profiles", "openrouter")
	if failed.Code == 0 || !strings.Contains(failed.Stderr, "keychain") {
		t.Fatalf("no-keyring rotate = (%d, %s)", failed.Code, failed.Stderr)
	}
	if got := openrouterFingerprint(t, e, "p1"); got != after1 {
		t.Errorf("p1 fingerprint changed on keyring failure: %q -> %q", after1, got)
	}
	if got := openrouterFingerprint(t, e, "p2"); got != after2 {
		t.Errorf("p2 fingerprint changed on keyring failure: %q -> %q", after2, got)
	}
}

func requireRotateAllOutput(t *testing.T, res Result, before1, after1, before2, after2 string) {
	t.Helper()
	if strings.Contains(res.Stdout+res.Stderr, "brand-new-value") {
		t.Fatal("rotate printed the secret value")
	}
	if !strings.Contains(res.Stdout, "rotated p1\n") || !strings.Contains(res.Stdout, "rotated p2\n") ||
		!strings.Contains(res.Stdout, "skipped p3: no entry\n") {
		t.Fatalf("rotate stdout = %q", res.Stdout)
	}
	if after1 == before1 || after2 == before2 {
		t.Fatalf("fingerprints did not change: %q %q -> %q %q", before1, before2, after1, after2)
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
	res := runSecret(t, e, bin, "", nil, "setup", "secrets", "set", "--all-profiles", "token")
	if res.Code != 2 {
		t.Fatalf("set --all-profiles = (%d, %s), want usage error", res.Code, res.Stderr)
	}
}
