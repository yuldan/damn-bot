package budget

import "math"

// Tolerance = toleransi reproduksi BASELINE (ROADMAP F0 acceptance: ±5%).
const Tolerance = 0.05

// Check = satu angka BASELINE-hermes.md vs hasil ukur harness.
type Check struct {
	Name   string  `json:"name"`
	Source string  `json:"source"` // rujukan angka acuan
	Want   int     `json:"want"`
	Got    int     `json:"got"`
	Delta  float64 `json:"delta"` // (got-want)/want
	// Gate: true = metrik acceptance F0 (overhead per call, schema bytes, tier); false = informatif.
	Gate bool   `json:"gate"`
	Note string `json:"note,omitempty"`
}

// OK melaporkan apakah |Delta| ≤ Tolerance.
func (c Check) OK() bool { return math.Abs(c.Delta) <= Tolerance }

func check(name, source string, want, got int, gate bool, note string) Check {
	return Check{
		Name: name, Source: source, Want: want, Got: got, Gate: gate, Note: note,
		Delta: float64(got-want) / float64(want),
	}
}

// Compare membandingkan snapshot default (cwd = root hermes, AGENTS.md dimuat) dan
// snapshot tanpa file konteks dengan angka BASELINE-hermes.md §2/§7.
// Kolom "Char" BASELINE (ADR-093, TS-06) = jumlah karakter Python (len(str)), bukan byte
// UTF-8: tier stable cocok persis 12.481 karakter (byte UTF-8 = 12.561).
func Compare(def, noCtx Snapshot) []Check {
	const b2, b7 = "BASELINE-hermes §2", "BASELINE-hermes §7"
	withAgents := ceilDiv(33183, charsPerToken) + 9440 // ceil(Σ tier §2 / 4) + schema §2
	return []Check{
		check("stable_chars", b2, 12481, def.Stable.Chars, true, ""),
		check("context_chars", b2, 13413, def.Context.Chars, true, "memuat AGENTS.md + snapshot git workspace (bergantung mesin)"),
		check("volatile_chars", b2, 7289, def.Volatile.Chars, true, "memuat path HERMES_HOME + timestamp (bergantung mesin)"),
		check("stable_est_tokens", b2, 3121, def.Stable.EstTokens, true, ""),
		check("context_est_tokens", b2, 3354, def.Context.EstTokens, true, ""),
		check("volatile_est_tokens", b2, 1823, def.Volatile.EstTokens, true, ""),
		check("schema_bytes", b2, 37758, def.Schema.Bytes, true, "json.dumps(get_tool_definitions()) default ensure_ascii=True"),
		check("schema_bytes_remeasure", b7, 37765, def.Schema.Bytes, true, ""),
		check("schema_est_tokens", b2, 9440, def.Schema.EstTokens, true, ""),
		check("schema_count", b2, 19, def.SchemaCount, true, "harus persis"),
		check("delegate_task_schema_bytes", b2, 4671, def.PerTool["delegate_task"].Bytes, true, ""),
		check("overhead_floor_est_tokens", b2, 14500, noCtx.OverheadEst, true, "overhead per call tanpa AGENTS.md (prompt + schema)"),
		check("overhead_with_agents_est_tokens", b2+" (Σ tier) / SCORECARD §1.1", withAgents, def.OverheadEst, true, "overhead per call dengan AGENTS.md"),
		check("floor_prompt_chars", b2, 20240, noCtx.SystemPrompt.Chars, false,
			"komponen floor; sisa tier context tanpa AGENTS.md = snapshot git workspace yang bergantung mesin/commit"),
	}
}
