package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func hooksTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("APPDATA", home)
	t.Setenv("HOME", home)
	base := filepath.Join(home, "damnbot")
	if runtime.GOOS == "darwin" {
		base = filepath.Join(home, "Library", "Application Support", "damnbot")
	}
	dir := filepath.Join(base, "hooks")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	return dir
}

func hookExe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func TestConfigHooksListValidatesExecutable(t *testing.T) {
	dir := hooksTestHome(t)
	if err := os.WriteFile(filepath.Join(dir, hookExe("allow")), []byte("#!/bin/sh\n"), 0o700); err != nil { // #nosec G306 -- fixture must be executable
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plain"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runConfig([]string{"hooks", "ls"}, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%q", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{hookExe("allow") + "\tok", "plain\tnot executable"} {
		if !strings.Contains(got, want) {
			t.Errorf("output %q missing %q", got, want)
		}
	}
}

func TestConfigHooksTestRejectsBadNamesWithoutExecuting(t *testing.T) {
	dir := hooksTestHome(t)
	outside := filepath.Join(filepath.Dir(dir), "outside")
	marker := filepath.Join(t.TempDir(), "ran")
	script := "#!/bin/sh\ntouch " + marker + "\nprintf '{\"decision\":\"allow\"}'\n"
	if err := os.WriteFile(outside, []byte(script), 0o700); err != nil { // #nosec G306 -- fixture must be executable
		t.Fatal(err)
	}
	for _, name := range []string{"../outside", "../hooks/../outside", "a/b", ".."} {
		var out, errOut bytes.Buffer
		if code := runConfig([]string{"hooks", "test", name}, &out, &errOut); code != 2 {
			t.Errorf("test %q exit = %d, want 2; stderr=%q", name, code, errOut.String())
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("path traversal executed the outside hook: stat err=%v", err)
	}
	var out, errOut bytes.Buffer
	if code := runConfig([]string{"hooks", "test", "missing"}, &out, &errOut); code != 1 {
		t.Errorf("missing exit = %d, want 1", code)
	}
}

func TestConfigHooksTestRunsDummyInput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script hook is POSIX-only")
	}
	dir := hooksTestHome(t)
	script := `#!/bin/sh
read -r line || true
case "$1:$line" in
gateway_inbound:*damnbot.hook/v1*) printf '{"decision":"allow"}' ;;
*) printf '{"decision":"deny"}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "check"), []byte(script), 0o700); err != nil { // #nosec G306 -- fixture must be executable
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runConfig([]string{"hooks", "test", "check"}, &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%q stdout=%q", code, errOut.String(), out.String())
	}
	for _, want := range []string{"hook\tcheck", "event\tgateway_inbound", "decision\tallow", "exit\t0"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output %q missing %q", out.String(), want)
		}
	}
}
