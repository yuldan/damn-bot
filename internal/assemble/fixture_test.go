package assemble

import (
	"testing"
	"time"
)

// asciiCounter matches budget.Count(...).Raw for ASCII text: every Latin
// rune (including ASCII punctuation and digits) is 1/4 token, rounded up.
// The P1 fixture is ASCII by construction, so this is exactly the production
// estimator for the fixture bytes; non-ASCII production text is counted by the
// injected budget counter.
func asciiCounter() Counter {
	return CounterFunc(func(s string) int { return (len(s) + 3) / 4 })
}

func newAssembler(t *testing.T, scrub func(string) string) *Assembler {
	t.Helper()
	a, err := New(Deps{Tokens: asciiCounter(), Scrub: scrub})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func identity(s string) string { return s }

func fixtureVolatile() *VolatileInput {
	todos := []Todo{
		{Text: "Fix the failing test in internal/foo with the race detector enabled and verify", Status: "in_progress"},
		{Text: "Update the golden fixture and read the diff before committing the change", Status: "pending"},
		{Text: "Run go test ./internal/assemble and report the measured token budget", Status: "pending"},
		{Text: "This completed item must not render", Status: "done"},
		{Text: "A fourth open item is hidden behind +N more", Status: "pending"},
	}
	return &VolatileInput{
		Now:             time.Date(2026, 10, 19, 21, 0, 0, 0, time.UTC),
		RemainingTokens: 123456,
		RemainingUSD:    0.42,
		Todos:           todos,
		NewNames:        []string{"skill://code-review", "tool://grep", "skill://tdd", "tool://edit_file"},
		Notices: []string{
			"memory provider offline; using the local store",
			"process npm test still running",
			"workspace changed on disk",
		},
	}
}
