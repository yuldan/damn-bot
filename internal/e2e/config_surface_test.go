package e2e

import (
	"strings"
	"testing"
)

// noCrash is the negative control every honest-unavailable surface must pass:
// no panic text and no Go stack trace on either stream.
func noCrash(t *testing.T, res Result) {
	t.Helper()
	for _, s := range []string{res.Stdout, res.Stderr} {
		for _, bad := range []string{"panic:", "goroutine ", "runtime/debug.Stack"} {
			if strings.Contains(s, bad) {
				t.Errorf("output contains %q:\n%s", bad, s)
			}
		}
	}
}

// TestConfigModulesSurface: the real binary lists exactly the compiled-in
// registry (empty in F3), explains why [modules.<id>] is rejected, and never
// invents module rows.
func TestConfigModulesSurface(t *testing.T) {
	t.Parallel()
	e := New(t)
	res := e.Exec("config", "modules")
	res.RequireOK(t)
	noCrash(t, res)
	if res.Stderr != "" {
		t.Errorf("stderr = %q, want empty", res.Stderr)
	}
	if !strings.HasPrefix(res.Stdout, "modules: 0 compiled in\n") {
		t.Errorf("stdout = %q, want the empty registry count", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "module section unavailable") {
		t.Errorf("stdout lacks the [modules.<id>] note:\n%s", res.Stdout)
	}
	if strings.Contains(res.Stdout, "id=") {
		t.Errorf("fake module row in an empty registry:\n%s", res.Stdout)
	}
}

// TestConfigModulesRestartRefused: restart needs the F4/F5 serve engine, so it
// exits 2 with a reason and no stdout.
func TestConfigModulesRestartRefused(t *testing.T) {
	t.Parallel()
	e := New(t)
	res := e.Exec("config", "modules", "restart", "browser")
	noCrash(t, res)
	if res.Code != 2 {
		t.Fatalf("exit = %d, want 2\nstdout:\n%s\nstderr:\n%s", res.Code, res.Stdout, res.Stderr)
	}
	if res.Stdout != "" {
		t.Errorf("stdout = %q, want empty", res.Stdout)
	}
	if !strings.Contains(res.Stderr, `module "browser" was not restarted`) || !strings.Contains(res.Stderr, "F4/F5") {
		t.Errorf("stderr lacks the unavailable reason: %s", res.Stderr)
	}
}

// TestConfigHooksRefused: ls and test refuse because internal/hooks and the
// damnbot.hook/v1 engine do not exist (F4).
func TestConfigHooksRefused(t *testing.T) {
	t.Parallel()
	e := New(t)
	cases := [][]string{
		{"config", "hooks", "ls"},
		{"config", "hooks", "test", "pre_tool"},
	}
	for _, args := range cases {
		res := e.Exec(args...)
		noCrash(t, res)
		if res.Code != 2 {
			t.Errorf("%q exit = %d, want 2\nstderr:\n%s", args, res.Code, res.Stderr)
		}
		if res.Stdout != "" {
			t.Errorf("%q stdout = %q, want empty", args, res.Stdout)
		}
		if !strings.Contains(res.Stderr, "hook engine") || !strings.Contains(res.Stderr, "F4") {
			t.Errorf("%q stderr lacks the F4 reason: %s", args, res.Stderr)
		}
	}
}
