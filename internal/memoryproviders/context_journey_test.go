package memoryproviders

import (
	"errors"
	"testing"
	"time"
)

// TestF8MEM023ContextEngine: the slot is default off, only the built-in
// compressor resolves, and rendering never mutates the caller's view.
func TestF8MEM023ContextEngine(t *testing.T) {
	t.Parallel()
	if _, err := LookupContextEngine(""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("LookupContextEngine(\"\") = %v, want ErrUnavailable", err)
	}
	if _, err := LookupContextEngine("other"); !errors.Is(err, ErrUnknownEngine) {
		t.Fatalf("LookupContextEngine(other) = %v, want ErrUnknownEngine", err)
	}
	engine, err := LookupContextEngine(ContextEngineCompressor)
	if err != nil || engine.Name() != ContextEngineCompressor {
		t.Fatalf("LookupContextEngine(compressor) = %v, %v", engine, err)
	}
	in := ContextInput{
		System:  "s",
		Summary: "sum",
		Messages: []ContextMessage{
			{Role: "user", Content: "hello"},
		},
	}
	out, err := engine.Render(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Messages) != 1 || out.Messages[0] != in.Messages[0] {
		t.Fatalf("render = %+v", out)
	}
	out.Messages[0].Content = "changed"
	if in.Messages[0].Content != "hello" {
		t.Fatal("engine mutated the input")
	}
}

// TestF8MEM035LearningJourney: the local timeline is chronological,
// duplicate-free, and redacts an entry without a source.
func TestF8MEM035LearningJourney(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	events := []JourneyEvent{
		{At: t0.Add(2 * time.Hour), Kind: JourneyMemory, ID: "m2", Title: "second memory", Source: "review"},
		{At: t0, Kind: JourneySkill, ID: "s1", Title: "secret skill", Source: ""},
		{At: t0.Add(time.Hour), Kind: JourneyMemory, ID: "m1", Title: "first memory", Source: "user"},
		{At: t0, Kind: JourneySkill, ID: "s1", Title: "duplicate", Source: "review"},
	}
	got, err := BuildJourney(events, 10)
	if err != nil {
		t.Fatalf("BuildJourney: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("entries = %+v, want 3 after dedupe", got)
	}
	if got[0].ID != "s1" || got[1].ID != "m1" || got[2].ID != "m2" {
		t.Fatalf("order = %+v", got)
	}
	if got[0].Title != redactedJourney || got[0].Source != redactedJourney {
		t.Fatalf("unsourced entry was not redacted: %+v", got[0])
	}
	if got[1].Title != "first memory" {
		t.Fatalf("sourced entry changed: %+v", got[1])
	}
	if _, err := BuildJourney([]JourneyEvent{{At: t0, Kind: "other", ID: "x"}}, 0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid kind = %v, want ErrInvalid", err)
	}
}
