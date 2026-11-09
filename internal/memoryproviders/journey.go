package memoryproviders

import (
	"fmt"
	"slices"
	"time"
)

// JourneyKind is the learned-item family.
type JourneyKind string

// Journey families (MEM-035).
const (
	JourneySkill  JourneyKind = "skill"
	JourneyMemory JourneyKind = "memory"
)

// MaxJourneyEntries bounds one local timeline.
const MaxJourneyEntries = 500

// redactedJourney replaces a title whose entry has no source.
const redactedJourney = "[redacted]"

// JourneyEvent is one locally recorded learning event. Nothing here is
// uploaded automatically: the journey is a local summary.
type JourneyEvent struct {
	At     time.Time
	Kind   JourneyKind
	ID     string
	Title  string
	Source string
}

// JourneyEntry is one rendered timeline row.
type JourneyEntry struct {
	At     time.Time
	Kind   JourneyKind
	ID     string
	Title  string
	Source string
}

// BuildJourney returns a chronological, duplicate-free timeline. Duplicates
// are the same (kind, id); the earliest event wins. An entry without a source
// keeps its row but has its title redacted. The result never exceeds limit
// (default [MaxJourneyEntries]).
func BuildJourney(events []JourneyEvent, limit int) ([]JourneyEntry, error) {
	if limit <= 0 || limit > MaxJourneyEntries {
		limit = MaxJourneyEntries
	}
	sorted := append([]JourneyEvent(nil), events...)
	slices.SortStableFunc(sorted, func(a, b JourneyEvent) int {
		if c := a.At.Compare(b.At); c != 0 {
			return c
		}
		if c := slices.Compare([]string{string(a.Kind), a.ID}, []string{string(b.Kind), b.ID}); c != 0 {
			return c
		}
		return 0
	})
	seen := make(map[string]bool, len(sorted))
	out := make([]JourneyEntry, 0, min(len(sorted), limit))
	for _, e := range sorted {
		if e.Kind != JourneySkill && e.Kind != JourneyMemory {
			return nil, fmt.Errorf("%w: journey kind %q", ErrInvalid, e.Kind)
		}
		if e.ID == "" || e.At.IsZero() {
			return nil, fmt.Errorf("%w: journey entry needs id and time", ErrInvalid)
		}
		key := string(e.Kind) + ":" + e.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		entry := JourneyEntry(e)
		if entry.Source == "" {
			entry.Title, entry.Source = redactedJourney, redactedJourney
		}
		out = append(out, entry)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}
