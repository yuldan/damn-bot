package hub

import (
	"fmt"
	"slices"
	"strings"

	"github.com/damnbot/damnbot/internal/skills"
)

// UsageEntry is one skill's aggregate ledger activity. It deliberately holds
// no path, description, body, or prompt text: usage is an input for the
// curator, not a second copy of the catalog.
type UsageEntry struct {
	Name        string
	Indexed     int
	Quarantined int
	Approved    int
	Rejected    int
	Other       int
	LastAt      int64
}

// UsageReport is the read-only usage view.
type UsageReport struct {
	Entries []UsageEntry
	Total   int
}

// ReadUsage aggregates <dataDir>/skills-ledger.jsonl by skill name and action.
// It only reads the ledger; it never writes, and the returned rows contain
// counts and timestamps only (no body or prompt content).
func ReadUsage(dataDir string) (UsageReport, error) {
	entries, err := skills.ReadLedger(dataDir)
	if err != nil {
		return UsageReport{}, fmt.Errorf("skills hub: usage: %w", err)
	}
	byName := make(map[string]*UsageEntry, len(entries))
	var rep UsageReport
	for _, e := range entries {
		u, ok := byName[e.Name]
		if !ok {
			u = &UsageEntry{Name: e.Name}
			byName[e.Name] = u
			rep.Entries = append(rep.Entries, *u)
		}
		switch strings.ToLower(e.Action) {
		case "indexed":
			u.Indexed++
		case "quarantined":
			u.Quarantined++
		case "approved":
			u.Approved++
		case "rejected":
			u.Rejected++
		default:
			u.Other++
		}
		if e.At > u.LastAt {
			u.LastAt = e.At
		}
	}
	for i := range rep.Entries {
		if u, ok := byName[rep.Entries[i].Name]; ok {
			rep.Entries[i] = *u
		}
		rep.Total += rep.Entries[i].Indexed + rep.Entries[i].Quarantined +
			rep.Entries[i].Approved + rep.Entries[i].Rejected + rep.Entries[i].Other
	}
	slices.SortFunc(rep.Entries, func(a, b UsageEntry) int { return strings.Compare(a.Name, b.Name) })
	return rep, nil
}
