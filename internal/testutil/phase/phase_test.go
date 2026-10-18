package phase

import (
	"fmt"
	"strings"
	"testing"
)

// TestPhaseFile pins the repo-root PHASE file: one line F1..F9 (ADR-077).
func TestPhaseFile(t *testing.T) {
	if g := Current(t); g.cur < 1 || g.cur > 9 {
		t.Fatalf("PHASE GAGAL: fase %d", g.cur)
	}
}

func TestParse(t *testing.T) {
	for in, ok := range map[string]bool{"F1": true, "F9\n": true, "F0": false, "F10": false, "f1": false, "F1\n\n": false, "": false} {
		if _, err := Parse(in); (err == nil) != ok {
			t.Errorf("Parse(%q) err = %v, want ok %v", in, err, ok)
		}
	}
}

// errs records Errorf calls.
type errs struct {
	testing.TB
	msgs []string
}

func (e *errs) Helper() {}

func (e *errs) Errorf(format string, args ...any) {
	e.msgs = append(e.msgs, fmt.Sprintf(format, args...))
}

func TestItem(t *testing.T) {
	cases := []struct {
		cur        int
		acceptance bool
		phase      string
		status     string
		run        bool
		fail       string // substring of the expected error; "" = no error
	}{
		{cur: 1, phase: "F1", status: "pending:F1"},
		{cur: 1, phase: "F1", status: "covered", run: true},
		{cur: 1, phase: "F2", status: "pending:F2"},
		{cur: 1, phase: "F2", status: "covered", run: true}, // early coverage (NEW2-22)
		{cur: 1, phase: "F3", status: "n/a", run: true},
		{cur: 2, phase: "F1", status: "pending:F1", fail: "must be covered"},
		{cur: 1, phase: "F2", status: "pending:F1", fail: "want covered"},
		{cur: 1, phase: "F1", status: "todo", fail: "want covered"},
		{cur: 1, phase: "X1", status: "covered", fail: "bukan F1..F9"},
		{cur: 1, acceptance: true, phase: "F1", status: "pending:F1", fail: "phase acceptance"},
		{cur: 1, acceptance: true, phase: "F2", status: "pending:F2"},
		{cur: 9, phase: "F9", status: "pending:F9", fail: "pending at PHASE F9"},
	}
	for _, c := range cases {
		e := &errs{TB: t}
		run := Gate{cur: c.cur, acceptance: c.acceptance}.Item(e, "ID", c.phase, c.status)
		got := strings.Join(e.msgs, "; ")
		if run != c.run || (c.fail == "") != (got == "") || !strings.Contains(got, c.fail) {
			t.Errorf("F%d acc=%v %s %q: run=%v errs=%q, want run=%v fail~%q", c.cur, c.acceptance, c.phase, c.status, run, got, c.run, c.fail)
		}
	}
}
