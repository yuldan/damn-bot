package compaction

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/budget"
)

var update = flag.Bool("update", false, "rewrite golden files") //nolint:gochecknoglobals // test-only golden flag

// fixture is a deterministic coding session of at least 150K tokens
// (COST-09 count of the message contents): turns of reads, searches, edits
// and answers. userRunes sizes each request, srcLines each file read.
func fixture(t *testing.T, turns, userRunes, srcLines int) []Event {
	t.Helper()
	b := transcript{t: t}
	for i := range turns {
		file := fmt.Sprintf("pkg/f%d.go", i%7)
		b.user(fmt.Sprintf("turn %d: refactor %s. ", i, file) + strings.Repeat("context ", userRunes/8))
		var src strings.Builder
		for l := range srcLines {
			fmt.Fprintf(&src, "func f%d_%d() int { return %d } // %s\n", i%7, l, l, file)
		}
		b.call("read_file", `{"path":"`+file+`"}`, src.String())
		b.call("grep", `{"pattern":"TODO"}`, strings.Repeat(file+":1: TODO tidy\n", 60))
		b.assistant(fmt.Sprintf("Editing %s now. ", file)+strings.Repeat("reasoning ", 40),
			ToolCall{ID: fmt.Sprintf("e%d", i), Name: "edit_file", Arguments: `{"path":"` + file + `"}`})
		b.result(fmt.Sprintf("e%d", i), "edit_file", "ok", `{}`)
		b.assistant(fmt.Sprintf("Done with %s. ", file) + strings.Repeat("summary ", 30))
	}
	if n := tokens(b.events); n < 150_000 {
		t.Fatalf("fixture = %d tokens, want >= 150000", n)
	}
	return b.events
}

func tokens(events []Event) int64 {
	var n int64
	for i := range events {
		n += budget.Count(events[i].Content).Raw()
	}
	return n
}

// TestCompaction is the P8 acceptance on a 150K-token fixture: stage 1
// alone, stage 2 after an insufficient prune, and context_overflow when
// the request stays above 90 % after both (SPEC §4.7, ADR-122).
func TestCompaction(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                       string
		turns, userRunes, srcLines int
		overhead, window           int64
		outcome                    Outcome
		overflow                   bool
	}{
		{"prune", 40, 400, 300, 4_000, 200_000, OutcomePruned, false},
		{"summary", 40, 11_600, 60, 4_000, 200_000, OutcomeSummarized, false},
		{"overflow", 40, 11_600, 60, 185_000, 200_000, OutcomeIneffective, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t, tokenMeter{overhead: c.overhead, window: c.window}, replyWith(validSummary))
			evs := fixture(t, c.turns, c.userRunes, c.srcLines)
			r.store.put("s", evs)
			raw, err := r.c.View(t.Context(), "s")
			if err != nil {
				t.Fatal(err)
			}
			before, _, err := tokenMeter{overhead: c.overhead}.Measure(t.Context(), "s", &raw)
			if err != nil {
				t.Fatal(err)
			}
			if Default().Decide(before, c.window).Kind == None {
				t.Fatalf("fixture is below compact_at: %d/%d", before, c.window)
			}
			res := r.run(t, "s")
			v, err := r.c.View(t.Context(), "s")
			if err != nil {
				t.Fatal(err)
			}
			overflow := Default().Decide(res.Estimate, res.Window).Kind == Overflow
			if res.Outcome != c.outcome || overflow != c.overflow || r.sum.count() > 1 {
				t.Fatalf("result = %+v, overflow %v, summary calls %d", res, overflow, r.sum.count())
			}
			checkGolden(t, filepath.Join("testdata", "TestCompaction", c.name+".json"), goldenOf(t, tokens(evs), before, &res, &v, r))
		})
	}
}

// goldenOf serializes the acceptance result. Result.Usage includes the
// event contract's CacheCreation field (GAP-COST-03): the summary call may
// report a provider cache write, so the golden pins it (zero for this
// fixture's reply) instead of hiding it from the stored shape (ADR-266).
func goldenOf(t *testing.T, fixtureTokens, before int64, res *Result, v *View, r *rig) []byte {
	t.Helper()
	type msg struct {
		Role, Call string
		Runes      int
		Head       string
	}
	out := struct {
		FixtureTokens, Before int64
		Result                *Result
		SummaryInputTokens    int64
		Summaries             []Summary
		Messages              []msg
	}{FixtureTokens: fixtureTokens, Before: before, Result: res, Summaries: v.Summaries}
	if r.sum.count() > 0 {
		out.SummaryInputTokens = budget.Count(r.sum.last.Input).Raw()
	}
	for _, m := range v.Messages {
		id := m.ToolCallID
		for _, c := range m.ToolCalls {
			id += c.ID + ":" + c.Name + " "
		}
		head := []rune(m.Content)
		out.Messages = append(out.Messages, msg{Role: m.Role, Call: strings.TrimSpace(id), Runes: len(head), Head: string(head[:min(len(head), 60)])})
	}
	b, err := json.MarshalIndent(out, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func checkGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // test-only golden path
	if err != nil {
		t.Fatalf("read golden %s (run go test -update): %v", path, err)
	}
	if !bytes.Equal(got, bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))) {
		t.Errorf("%s differs (run go test -update and read the diff)", path)
	}
}
