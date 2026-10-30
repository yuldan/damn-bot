package e2e

import (
	"strings"
	"testing"
)

// TestE2EConfigEnvOverride (GAP-OPS-01): the real binary applies a generated
// DAMNBOT_<SECTION>_<KEY> override and reports env provenance; an unknown key
// is refused rather than silently accepted.
func TestE2EConfigEnvOverride(t *testing.T) {
	e := New(t)
	env := []string{"DAMNBOT_LOOP_MAX_ITERATIONS=7"}
	res := e.ExecEnv(env, "config", "get", "loop.max_iterations")
	res.RequireOK(t)
	if got := strings.TrimSpace(res.Stdout); got != "7" {
		t.Fatalf("config get = %q, want 7", got)
	}
	res = e.ExecEnv(env, "config", "explain", "loop.max_iterations")
	res.RequireOK(t)
	if !strings.Contains(res.Stdout, "env DAMNBOT_LOOP_MAX_ITERATIONS") {
		t.Fatalf("explain lacks env source:\n%s", res.Stdout)
	}
	res = e.Exec("config", "get", "no.such.key")
	if res.Code == 0 || !strings.Contains(res.Stderr, "unknown key") {
		t.Fatalf("unknown key = %d\n%s", res.Code, res.Stderr)
	}
}
