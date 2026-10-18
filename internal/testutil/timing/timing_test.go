package timing

import (
	"fmt"
	"runtime"
	"testing"
	"time"
)

// recorder captures Fatalf/Logf of the helper under test; FailNow-style
// termination is modelled with runtime.Goexit inside its own goroutine.
type recorder struct {
	testing.TB
	fatal, logged string
}

func (r *recorder) Helper() {}

func (r *recorder) Logf(format string, args ...any) { r.logged = fmt.Sprintf(format, args...) }

func (r *recorder) Fatalf(format string, args ...any) {
	r.fatal = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// run executes f against a fresh recorder until it returns or "fails".
func run(t *testing.T, f func(tb testing.TB)) *recorder {
	t.Helper()
	r := &recorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		f(r)
	}()
	<-done
	return r
}

func TestCheck(t *testing.T) {
	cases := []struct {
		name, env      string
		got, limit     time.Duration
		fatal, logOnly bool
	}{
		{name: "not enforced logs", env: "", got: time.Second, limit: time.Millisecond, logOnly: true},
		{name: "enforced within", env: "1", got: time.Millisecond, limit: time.Second},
		{name: "enforced over", env: "1", got: time.Second, limit: time.Millisecond, fatal: true},
		// 1.25× the budget: over it as written, within the ×1.5 perf margin (ADR-077).
		{name: "perf margin", env: "1", got: 1250 * time.Millisecond, limit: time.Second, fatal: scaleNum == scaleDen},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(Env, c.env)
			r := run(t, func(tb testing.TB) { Check(tb, "x", c.got, c.limit) })
			if RaceEnabled && c.env == "1" {
				// A -race build refuses enforcement outright (ADR-048).
				if r.fatal == "" {
					t.Fatal("enforcement under -race did not fail")
				}
				return
			}
			if (r.fatal != "") != c.fatal {
				t.Fatalf("fatal = %q, want fatal %v", r.fatal, c.fatal)
			}
			if (r.logged != "") != c.logOnly {
				t.Fatalf("logged = %q, want log %v", r.logged, c.logOnly)
			}
		})
	}
}
