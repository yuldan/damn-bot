package h2h

import (
	"errors"
	"testing"
)

// TestCategorizeRun: kontrak stop kosong tidak boleh jatuh ke upstream_unavailable
// (yang memicu retry berbayar); timeout dan exit error tetap dipetakan seperti semula.
func TestCategorizeRun(t *testing.T) {
	t.Parallel()
	runErr := errors.New("start failed")
	cases := []struct {
		name string
		run  AgentRun
		err  error
		want string
	}{
		{"timed out flag", AgentRun{TimedOut: true}, nil, "task_timeout"},
		{"timeout reason", AgentRun{StopReason: "timeout"}, nil, "task_timeout"},
		{"run error", AgentRun{}, runErr, "harness_error"},
		{"missing stop reason", AgentRun{ExitCode: 0}, nil, "harness_error"},
		{"upstream error", AgentRun{StopReason: "error"}, nil, "upstream_unavailable"},
		{"completed", AgentRun{StopReason: "end_turn", StopDetail: "completed"}, nil, "ok"},
		{"no progress", AgentRun{StopReason: "end_turn", StopDetail: "no_progress"}, nil, "no_progress"},
		{"other end_turn", AgentRun{StopReason: "end_turn", StopDetail: "budget"}, nil, "model_other"},
		{"unknown reason", AgentRun{StopReason: "weird"}, nil, "harness_error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := categorizeRun(c.run, Usage{}, c.err); got != c.want {
				t.Fatalf("categorizeRun = %q, want %q", got, c.want)
			}
		})
	}
}

// TestRetryable: hanya upstream_unavailable yang diulang; kategori sukses dan
// harness_error tidak boleh membakar cap dengan attempt identik.
func TestRetryable(t *testing.T) {
	t.Parallel()
	want := map[string]bool{
		"upstream_unavailable": true,
		"ok":                   false,
		"no_progress":          false,
		"model_other":          false,
		"task_timeout":         false,
		"harness_error":        false,
		"":                     false,
	}
	for category, expected := range want {
		if got := retryable(category); got != expected {
			t.Fatalf("retryable(%q) = %v, want %v", category, got, expected)
		}
	}
}
