package hub

import (
	"slices"
	"strings"
	"time"
)

// CuratorMinIdle is the idle gate of SPEC §9.11: background curation may run
// only after the process has been idle for at least two hours.
const CuratorMinIdle = 2 * time.Hour

// Curator defaults.
const (
	DefaultStaleAfter = 30 * 24 * time.Hour
	DefaultMaxActions = 50
)

// CuratorConfig is the deterministic curator policy. Now and the clock are
// seams; zero values get production defaults.
type CuratorConfig struct {
	Now        func() time.Time
	StaleAfter time.Duration
	MaxActions int
	// Budget caps the number of proposals in one run; 0 means the run is
	// disabled, not unlimited.
	Budget int
}

// CuratorProposal is one maintenance suggestion. It is never applied by this
// package: the caller routes it to quarantine/review (P10).
type CuratorProposal struct {
	Name   string
	Action string // archive|retire|merge
	Reason string
}

// Curate derives archive/retire/merge proposals from the read-only usage
// report. It runs 0 LLM calls, writes nothing, and returns nil before two
// hours of idle time. A proposal is made only for an entry whose last ledger
// activity is older than StaleAfter.
func Curate(rep UsageReport, lastActivity time.Time, cfg CuratorConfig) []CuratorProposal {
	now := time.Now
	if cfg.Now != nil {
		now = cfg.Now
	}
	stale := cfg.StaleAfter
	if stale <= 0 {
		stale = DefaultStaleAfter
	}
	maxActions := cfg.MaxActions
	if maxActions <= 0 {
		maxActions = DefaultMaxActions
	}
	limit := min(cfg.Budget, maxActions)
	if limit <= 0 || lastActivity.IsZero() || now().Sub(lastActivity) < CuratorMinIdle {
		return nil
	}
	entries := slices.Clone(rep.Entries)
	slices.SortFunc(entries, func(a, b UsageEntry) int { return strings.Compare(a.Name, b.Name) })
	known := make(map[string]bool, len(entries))
	for _, e := range entries {
		known[e.Name] = true
	}
	cutoff := now().Add(-stale).Unix()
	out := make([]CuratorProposal, 0, limit)
	for _, e := range entries {
		if len(out) >= limit {
			break
		}
		if e.LastAt > cutoff {
			continue
		}
		if p, ok := propose(e, known); ok {
			out = append(out, p)
		}
	}
	return out
}

// propose classifies one stale entry. Retire wins for a rejected/quarantined
// entry; a stale -slim variant merges into its base; anything else that was
// ever indexed/approved is archived.
func propose(e UsageEntry, known map[string]bool) (CuratorProposal, bool) {
	if e.Rejected > 0 || e.Quarantined > 0 {
		return CuratorProposal{Name: e.Name, Action: "retire", Reason: "quarantined or rejected and stale"}, true
	}
	if base, ok := strings.CutSuffix(e.Name, "-slim"); ok && known[base] {
		return CuratorProposal{Name: e.Name, Action: "merge", Reason: "slim variant stale; merge into " + base}, true
	}
	if known[e.Name+"-slim"] {
		return CuratorProposal{}, false // the slim variant carries the merge proposal
	}
	if e.Indexed > 0 || e.Approved > 0 {
		return CuratorProposal{Name: e.Name, Action: "archive", Reason: "no activity within the stale window"}, true
	}
	return CuratorProposal{}, false
}
