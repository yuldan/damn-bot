package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCompletionScripts(t *testing.T) {
	t.Parallel()
	e := New(t)
	bash := e.Exec("setup", "completion", "bash")
	bash.RequireOK(t)
	check := exec.CommandContext(t.Context(), "bash", "-n")
	check.Stdin = strings.NewReader(bash.Stdout)
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
	sourced := exec.CommandContext(t.Context(), "bash", "-c",
		`source /dev/stdin; COMP_WORDS=(damnbot se); COMP_CWORD=1; _damnbot_complete; printf "%s\n" "${COMPREPLY[@]}"`)
	sourced.Stdin = strings.NewReader(bash.Stdout)
	sourced.Env = append(os.Environ(), "PATH="+filepath.Dir(binPath)+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := sourced.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "sessions") || !strings.Contains(string(out), "setup") {
		t.Fatalf("sourced completion = %v %q", err, out)
	}
	if runtime.GOOS != "windows" {
		checkZsh(t, e)
	}
	for _, shell := range []string{"fish", "powershell"} {
		res := e.Exec("setup", "completion", shell)
		res.RequireOK(t)
		if !strings.Contains(res.Stdout, "__complete") || !strings.Contains(res.Stdout, "damnbot") {
			t.Fatalf("%s script incomplete:\n%s", shell, res.Stdout)
		}
	}
}

func TestCompleteCLI(t *testing.T) {
	t.Parallel()
	e := New(t)
	res := e.Exec("__complete")
	res.RequireOK(t)
	if !strings.Contains(res.Stdout, "sessions") || !strings.Contains(res.Stdout, "setup") {
		t.Fatalf("__complete = %q", res.Stdout)
	}
	res = e.Exec("__complete", "zzz")
	res.RequireOK(t)
	if res.Stdout != "" {
		t.Fatalf("no-candidate output = %q", res.Stdout)
	}
	res = e.Exec("__complete", "config", "")
	res.RequireOK(t)
	if !strings.Contains(res.Stdout, "show") || !strings.Contains(res.Stdout, "profiles") {
		t.Fatalf("config candidates = %q", res.Stdout)
	}
}

func checkZsh(t *testing.T, e *Env) {
	t.Helper()
	if _, err := exec.LookPath("zsh"); err != nil {
		skipOrFail(t, "zsh not installed")
		return
	}
	zsh := e.Exec("setup", "completion", "zsh")
	zsh.RequireOK(t)
	check := exec.CommandContext(t.Context(), "zsh", "-n")
	check.Stdin = strings.NewReader(zsh.Stdout)
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("zsh -n: %v\n%s", err, out)
	}
}
