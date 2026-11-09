//go:build !minimal

package config

import "math"

// ModulesAuto is [modules.auto] (SPEC §9.11, ADR-333): one closed section
// per parity feature. Every feature is off by default; enabling one requires
// an explicit daily budget, and the runtime lane tags its calls with
// purpose=auto and feature=<id>.
type ModulesAuto struct {
	Review      ModulesAutoFeature `toml:"review"`
	Title       ModulesAutoFeature `toml:"title"`
	Curator     ModulesAutoFeature `toml:"curator"`
	Compaction  ModulesAutoFeature `toml:"compaction"`
	Blueprints  ModulesAutoFeature `toml:"blueprints"`
	Suggestions ModulesAutoFeature `toml:"suggestions"`
	Goals       ModulesAutoFeature `toml:"goals"`
	Loops       ModulesAutoFeature `toml:"loops"`
	Heartbeat   ModulesAutoFeature `toml:"heartbeat"`
	DiskCleanup ModulesAutoFeature `toml:"disk_cleanup"`
	Maintenance ModulesAutoFeature `toml:"maintenance"`
	Webhooks    ModulesAutoFeature `toml:"webhooks"`
}

// ModulesAutoFeature is [modules.auto.<feature>]. DailyTokens and DailyUSD
// are the per-feature caps; zero means "use the registry default" for
// tokens and "no separate USD cap" for USD. MinIntervalS is honoured by the
// loop and heartbeat features (loops require >= 30).
type ModulesAutoFeature struct {
	Enabled      bool    `toml:"enabled" json:"enabled"`
	DailyTokens  int64   `toml:"daily_tokens" json:"daily_tokens"`
	DailyUSD     float64 `toml:"daily_usd" json:"daily_usd"`
	MinIntervalS int     `toml:"min_interval_s" json:"min_interval_s"`
}

// defaultAuto keeps every feature off and every budget at zero: zero means
// "use the registry default from SPEC §9.11" and the runtime fills it.
// Config reference therefore shows consent (enabled) as the only thing a
// user must write, while the budget remains an explicit opt-in too.
func defaultAuto() ModulesAuto { return ModulesAuto{} }

type autoRow struct {
	name string
	val  *ModulesAutoFeature
	// minInterval is the smallest accepted non-zero MinIntervalS; 0 means
	// the key is not used by that feature and must stay zero.
	minInterval int
}

func (a *ModulesAuto) rows() []autoRow {
	return []autoRow{
		{"review", &a.Review, 0},
		{"title", &a.Title, 0},
		{"curator", &a.Curator, 0},
		{"compaction", &a.Compaction, 0},
		{"blueprints", &a.Blueprints, 0},
		{"suggestions", &a.Suggestions, 0},
		{"goals", &a.Goals, 0},
		{"loops", &a.Loops, 30},
		{"heartbeat", &a.Heartbeat, 60},
		{"disk_cleanup", &a.DiskCleanup, 0},
		{"maintenance", &a.Maintenance, 0},
		{"webhooks", &a.Webhooks, 0},
	}
}

// validate keeps the config fail-closed: no negative or non-finite budget,
// no enabled feature without a cap, and no loop/heartbeat interval below the
// parity floor.
func (a *ModulesAuto) validate() error {
	for _, r := range a.rows() {
		f := r.val
		if f.DailyTokens < 0 {
			return invalid("modules.auto."+r.name+".daily_tokens", "must be >= 0")
		}
		if math.IsNaN(f.DailyUSD) || math.IsInf(f.DailyUSD, 0) || f.DailyUSD < 0 {
			return invalid("modules.auto."+r.name+".daily_usd", "must be a finite value >= 0")
		}
		if f.MinIntervalS < 0 {
			return invalid("modules.auto."+r.name+".min_interval_s", "must be >= 0")
		}
		if r.minInterval > 0 {
			if f.MinIntervalS != 0 && f.MinIntervalS < r.minInterval {
				return invalid("modules.auto."+r.name+".min_interval_s", "must be >= %d", r.minInterval)
			}
		} else if f.MinIntervalS != 0 {
			return invalid("modules.auto."+r.name+".min_interval_s", "is not used by this feature")
		}
	}
	return nil
}

// validateAuto routes the shared validation to this build's section.
func (m *Modules) validateAuto() error { return m.Auto.validate() }

// autoModuleAvailable reports that this build owns the closed auto registry.
func autoModuleAvailable() bool { return true }
