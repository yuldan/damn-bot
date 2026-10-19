package assemble

import (
	"strings"
	"testing"
)

func TestRuntimeSuffixCaps(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, identity)
	parts := a.Runtime(RuntimeInput{
		Iteration:     39,
		MaxIterations: 40,
		NoProgress:    true,
		ProcessNotice: "a long process notice that must be clipped before the cap is exceeded",
	})
	c := asciiCounter()
	checks := []struct {
		name string
		text string
		cap  int
	}{
		{"progress", parts.Progress, CapRuntimeProgress},
		{"no_progress", parts.NoProgress, CapRuntimeNoProgress},
		{"process", parts.Process, CapRuntimeProcess},
	}
	for _, check := range checks {
		if n := c.Count(check.text); n > check.cap {
			t.Errorf("%s = %d tokens, cap %d", check.name, n, check.cap)
		}
	}
	text := a.RuntimeString(parts)
	if n := c.Count(text); n > CapRuntime {
		t.Errorf("runtime suffix = %d tokens, cap %d: %q", n, CapRuntime, text)
	}
	if !strings.HasPrefix(text, "<runtime>") || !strings.HasSuffix(text, "</runtime>") {
		t.Errorf("runtime suffix shape = %q", text)
	}
}

func TestRuntimeEmpty(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, identity)
	if got := a.RuntimeString(a.Runtime(RuntimeInput{})); got != "" {
		t.Errorf("empty runtime = %q", got)
	}
}

func TestRuntimeDropsProcessFirst(t *testing.T) {
	t.Parallel()
	a, err := New(Deps{
		Tokens: CounterFunc(func(s string) int { return len(s) }),
		Scrub:  identity,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	parts := a.Runtime(RuntimeInput{
		Iteration: 3, MaxIterations: 40, NoProgress: true, ProcessNotice: "process notice",
	})
	text := a.RuntimeString(parts)
	if strings.Contains(text, "proc:") {
		t.Errorf("process notice should be dropped to fit 40 tokens: %q", text)
	}
	if !strings.Contains(text, "3/40") {
		t.Errorf("progress missing: %q", text)
	}
}
