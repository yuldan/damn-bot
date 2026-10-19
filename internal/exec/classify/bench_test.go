package classify

import (
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/testutil/phase"
	"github.com/damnbot/damnbot/internal/testutil/timing"
)

// classifyBudgetMinRuns: the budget is asserted only from this iteration
// count (make perf runs 100; make test-timing runs 1 cold iteration).
const classifyBudgetMinRuns = 100

// command2K is a realistic ≥ 2 KiB compound command.
func command2K() string {
	var b strings.Builder
	for b.Len() < 2048 {
		b.WriteString("git diff --stat HEAD~1 -- src/main.go && grep -rn 'TODO' internal/ | head -n 20; ")
		b.WriteString("FOO=1 go test ./... -run 'Test(A|B)' > out.txt 2>&1 || cat \"$HOME/proj/out.txt\"; ")
	}
	return b.String()
}

// BenchmarkClassify (EXEC-AND-CODING §A.8/§A.9): a 2 KiB command is
// classified in < 1 ms.
func BenchmarkClassify(b *testing.B) {
	cmd := command2K()
	env := testEnv()
	b.SetBytes(int64(len(cmd)))
	b.ReportAllocs()
	for b.Loop() {
		if v := Classify(cmd, env); v.Class != Mutating {
			b.Fatalf("class = %s", v.Class)
		}
	}
	if b.N >= classifyBudgetMinRuns {
		timing.Check(b, "Classify 2 KiB command", b.Elapsed()/time.Duration(b.N), time.Millisecond)
	}
}

// TestEdgeCases (EDGE-CASES-hermes.md, ADR-077/080): entries owned by
// exec/classify.
func TestEdgeCases(t *testing.T) {
	t.Parallel()
	if phase.Current(t).Item(t, "EC-TERM-006", "F1", "covered") {
		t.Run("EC-TERM-006", ecUnscannableHasReason)
	}
}

// ecUnscannableHasReason: a command the classifier cannot analyse is
// labeled with a stated reason (shown in the prompt/denial), so the model
// learns why instead of retrying blindly.
func ecUnscannableHasReason(t *testing.T) {
	for _, cmd := range []string{"if (", "$CMD", "eval \"$X\"", "bash", "cat `x`", "echo x | python3", "cd /x && cat y"} {
		v := Classify(cmd, testEnv())
		if v.Class != Unknown || len(v.Reasons) == 0 || v.Reasons[0] == "" {
			t.Errorf("%q = %s %q, want unknown with a reason", cmd, v.Class, v.Reasons)
		}
	}
}
