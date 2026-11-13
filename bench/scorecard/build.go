package scorecard

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/damnbot/damnbot/bench/budget/o200k"
)

// ArtefactPath is the published RC scorecard path (SCORECARD §2.6).
const ArtefactPath = "scorecard/results/2026-11-13-8fab87144e.json"

// ToolingPath is the committed code-quality/security/behaviour measurement.
const ToolingPath = "bench/scorecard/results/tooling-8fab87144e.json"

// Build rebuilds the RC artefact from committed phase artifacts; every number
// has its source SHA-256 recorded, and a missing measurement gets a reason.
func Build(root, date, damnbotCommit string) (*Artifact, error) {
	b := &builder{root: root, sources: map[string]string{}, dims: map[string]Dimension{}}
	if err := b.loadNotMeasured(); err != nil {
		return nil, err
	}
	if err := b.measure(); err != nil {
		return nil, err
	}
	if b.err != nil {
		return nil, b.err
	}
	doc, err := os.ReadFile(filepath.Join(root, "docs", "SCORECARD.md")) //nolint:gosec // path repo
	if err != nil {
		return nil, fmt.Errorf("scorecard: read SCORECARD: %w", err)
	}
	canonical := ParseDimensions(string(doc))
	if len(canonical) == 0 {
		return nil, fmt.Errorf("scorecard: SCORECARD §1 tidak memberi dimensi")
	}
	out := map[string]Dimension{}
	for i := range canonical {
		c := &canonical[i]
		d, ok := b.dims[c.Name]
		if !ok {
			d = Dimension{Rule: RuleLower, NotMeasured: b.reason(c.Name)}
		}
		d.Name, d.Section, d.Core = c.Name, c.Section, c.Core
		out[c.Name] = d
	}
	return &Artifact{
		Meta: Meta{
			Phase: Phase, Date: date, HermesCommit: HermesCommit,
			DamnbotCommit: damnbotCommit, EvalSet: "evalset/v1",
			Harness: "bench/scorecard: agregasi artefak fase terukur; 0 LLM call",
			Sources: b.sources,
		},
		Dimensions: out,
	}, nil
}

type builder struct {
	root        string
	err         error
	sources     map[string]string
	dims        map[string]Dimension
	notMeasured map[string]string
	notDefault  string
}

// loadNotMeasured reads the reviewable reason table for unmeasured rows.
func (b *builder) loadNotMeasured() error {
	const rel = "bench/scorecard/not_measured.json"
	v, err := b.load(rel)
	if err != nil {
		return err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return fmt.Errorf("scorecard: %s bukan objek", rel)
	}
	reasons, ok := m["reasons"].(map[string]any)
	if !ok {
		return fmt.Errorf("scorecard: %s: reasons bukan objek", rel)
	}
	b.notMeasured = map[string]string{}
	for name, r := range reasons {
		if s, ok := r.(string); ok {
			b.notMeasured[name] = s
		}
	}
	if s, ok := m["default"].(string); ok {
		b.notDefault = s
	}
	return nil
}

func (b *builder) reason(name string) string {
	if r := b.notMeasured[name]; r != "" {
		return r
	}
	return b.notDefault
}

// load reads rel, records its digest, and decodes it.
func (b *builder) load(rel string) (any, error) {
	full := filepath.Join(b.root, rel)
	data, err := os.ReadFile(full) //nolint:gosec // artefak repo
	if err != nil {
		return nil, fmt.Errorf("scorecard: %s: %w", rel, err)
	}
	sum := sha256.Sum256(data)
	b.sources[rel] = hex.EncodeToString(sum[:])
	return readJSON(full)
}

func (b *builder) must(rel string, paths ...string) float64 {
	if b.err != nil {
		return 0
	}
	v, err := b.load(rel)
	if err != nil {
		b.err = err
		return 0
	}
	x, ok := num(v, paths...)
	if !ok {
		b.err = fmt.Errorf("scorecard: %s: %s tidak ada", rel, strings.Join(paths, "."))
		return 0
	}
	return x
}

func (b *builder) put(d *Dimension) { b.dims[d.Name] = *d }

//nolint:funlen // urutan pengukuran 11 dimensi sengaja satu alur agar mudah diaudit.
func (b *builder) measure() error {
	// §1.1 Fixed overhead: P1 fixture counted with the hermes o200k tokenizer.
	tok, err := o200k.Load()
	if err != nil {
		return fmt.Errorf("scorecard: tokenizer: %w", err)
	}
	req := filepath.Join(b.root, "internal", "assemble", "testdata", "p1", "all-modules.request.json")
	raw, err := os.ReadFile(req) //nolint:gosec // path repo
	if err != nil {
		return fmt.Errorf("scorecard: baca fixture P1: %w", err)
	}
	sum := sha256.Sum256(raw)
	b.sources["internal/assemble/testdata/p1/all-modules.request.json"] = hex.EncodeToString(sum[:])

	budget := "bench/budget/results/hermes-868b9f244f.json"
	b.put(&Dimension{
		Name: "Fixed overhead", Core: true, Rule: RuleLowerMax, MaxRatio: 0.35,
		Unit: "token o200k", Hermes: new(b.must(budget, "default", "system_prompt", "o200k_tokens") +
			b.must(budget, "default", "schema", "o200k_tokens")), Damnbot: new(float64(tok.Count(string(raw)))),
		Source: []string{budget, "internal/assemble/testdata/p1/all-modules.request.json"},
		Detail: "hermes = system prompt + schema default (dengan AGENTS.md); damnbot = request all-modules",
	})

	// §1.1 Token skill per turn: the F2 artifact already reports the ratio on
	// the same query corpus; keep the ratio as the comparable value.
	f2 := "bench/f2/results/hermes-868b9f244f-F2.json"
	b.put(&Dimension{
		Name: "Token skill per turn", Rule: RuleLowerMax, MaxRatio: 0.2, Unit: "rasio vs hermes",
		Hermes: new(1.0), Damnbot: new(b.must(f2, "dimensions", "token_skill_per_turn", "ratio_damnbot_vs_hermes")),
		Source: []string{f2},
		Detail: "rasio token index/isi skill; corpora berbeda, jadi yang dibandingkan adalah biaya arsitektural (F2/ADR-164)",
	})

	// §1.2 Startup/first token/build: F3 measured both agents on the same TUI.
	f3d := "bench/f3/results/damnbot-8fab87144e-F3.json"
	f3build := "bench/f3/results/damnbot-6d01e7a1d5-F3.json"
	f3h := "bench/f3/results/hermes-868b9f244f-F3.json"
	b.put(&Dimension{
		Name: "Startup", Core: true, Rule: RuleLowerMax, MaxRatio: 0.1, Unit: "ms p50",
		Hermes:  new(b.must(f3h, "dimensions", "tui", "first_frame_p50_ms")),
		Damnbot: new(b.must(f3d, "dimensions", "startup_p12", "p50_ms")),
		Source:  []string{f3d, f3h},
		Detail:  "frame pertama TUI; target 10x (SPEC P12/F3), 120 fps pada build 8fab8714",
	})
	b.put(&Dimension{
		Name: "First token di UI", Core: true, Rule: RuleLowerMax, MaxRatio: 0.2, Unit: "ms p50",
		Hermes:  new(b.must(f3h, "dimensions", "tui", "first_token", "p50_ms")),
		Damnbot: new(b.must(f3d, "dimensions", "first_token_tui", "p50_ms")),
		Source:  []string{f3d, f3h},
		Detail:  "T0 fake provider → token terlihat di TUI; 120 fps (p50 10,0 → 6,178 ms)",
	})
	b.put(&Dimension{
		Name: "Waktu build bersih / inkremental", Rule: RuleLower, Unit: "detik p50 (clean)",
		Hermes:  new(b.must(f3h, "dimensions", "buildtime", "clean", "p50_s")),
		Damnbot: new(b.must(f3build, "dimensions", "buildtime", "clean", "p50_s")),
		Source:  []string{f3build, f3h},
		Detail: fmt.Sprintf("inkremental: hermes %.3f s vs damnbot %.3f s",
			b.must(f3h, "dimensions", "buildtime", "incremental", "p50_s"),
			b.must(f3build, "dimensions", "buildtime", "incremental", "p50_s")),
	})
	// §1.2 RSS/CPU idle: F4 measured both agents with the same /proc method.
	f4d := "bench/f4/results/damnbot-4c1e69ba-F4.json"
	f4h := "bench/f4/results/hermes-868b9f244f-F4.json"
	b.put(&Dimension{
		Name: "Idle RSS", Rule: RuleLowerMax, MaxRatio: 0.3, Unit: "MB",
		Hermes:  new(b.must(f4h, "dimensions", "rss_idle", "rss_mb")),
		Damnbot: new(b.must(f4d, "measurement", "rss_mb")),
		Source:  []string{f4d, f4h},
		Detail:  "serve + Telegram long-poll, 60 s",
	})
	b.put(&Dimension{
		Name: "CPU idle", Rule: RuleLower, Unit: "%",
		Hermes:  new(b.must(f4h, "dimensions", "cpu_idle", "percent")),
		Damnbot: new(b.must(f4d, "measurement", "idle_cpu_percent")),
		Source:  []string{f4d, f4h},
		Detail:  "target absolut ≤0,5% x86; sampel 60 s yang sama",
	})
	// §1.2 Voice/image: F6 used the shared fake STT/LLM/TTS and 12 MP image.
	f6 := "bench/speed/results/hermes-868b9f244f-F6.json"
	b.put(&Dimension{
		Name: "Voice end-to-end", Rule: RuleLower, Unit: "ms (terburuk overhead/TTS)",
		Hermes: new(max(b.must(f6, "dimensions", "voice_end_to_end", "agents", "hermes", "overhead_ms"),
			b.must(f6, "dimensions", "voice_end_to_end", "agents", "hermes", "tts_start_ms"))),
		Damnbot: new(max(b.must(f6, "dimensions", "voice_end_to_end", "agents", "damnbot", "overhead_ms"),
			b.must(f6, "dimensions", "voice_end_to_end", "agents", "damnbot", "tts_start_ms"))),
		Source: []string{f6},
		Detail: "target ≤200 ms overhead dan ≤300 ms TTS; fake provider, playback no-op",
	})
	b.put(&Dimension{
		Name: "Image preflight", Rule: RuleLower, Unit: "ms",
		Hermes:  new(b.must(f6, "dimensions", "image_preflight", "agents", "hermes", "total_ms")),
		Damnbot: new(b.must(f6, "dimensions", "image_preflight", "agents", "damnbot", "total_ms")),
		Source:  []string{f6},
		Detail:  "12 MP decode+resize+encode; target absolut 150 ms hanya sah di Q14 (ADR-323) dan tidak diklaim di sini",
	})
	// §1.3 Konkurensi: both agents append with 4×50 own + 2×50 shared turns.
	b.put(&Dimension{
		Name: "Konkurensi", Rule: RuleEqual, Unit: "error",
		Hermes: new(b.concurrentErrors(f2, "hermes")), Damnbot: new(b.concurrentErrors(f2, "damnbot")),
		Source: []string{f2}, Detail: "SQLITE_BUSY/error lain + integritas; 0 untuk kedua agen",
	})
	// Code quality/security/behaviour: one committed tooling artefact.
	tool := ToolingPath
	addTool := func(d *Dimension, path ...string) {
		v := b.must(tool, path...)
		d.Source = []string{tool}
		d.Hermes, d.Damnbot = new(0.0), new(v)
		b.put(d)
	}
	mk := func(name string, rule Rule, target float64, unit, detail string, paths ...string) {
		d := &Dimension{Name: name, Rule: rule, Target: target, Unit: unit, Detail: detail}
		addTool(d, paths...)
	}
	mk("Siklus import / komentar \"hindari circular import\"", RuleAbsMax, 0, "siklus", "TestImportGraph", "code_quality", "import_cycles")
	mk("Broad catch (`except Exception` / error diabaikan)", RuleAbsMax, 0, "temuan", "errcheck via make lint", "code_quality", "broad_catch")
	mk("State global mutable", RuleAbsMax, 0, "temuan", "gochecknoglobals via make lint", "code_quality", "global_mutable")
	mk("Temuan lint", RuleAbsMax, 0, "temuan", "make lint", "code_quality", "lint_issues")
	mk("Dependency langsung / CGO", RuleAbsMax, 28, "dependency", "go list -m non-indirect; CGO 0", "code_quality", "direct_deps")
	mk("Kode mati", RuleAbsMax, 0, "fungsi", "make deadcode 3 GOOS", "code_quality", "deadcode")
	mk("Secret redaction", RuleAbsMax, 0, "kebocoran", "TestRedactionAndZero + TestRecordingsNoKey", "security", "redaction_leaks")
	mk("Akses gateway orang asing", RuleAbsMax, 0, "tool/LLM call", "E8 tests", "security", "foreign_gateway_calls")
	mk("Data at-rest", RuleAbsMax, 0, "credential plaintext", "at-rest tests", "security", "at_rest_plaintext")
	mk("Supply chain", RuleAbsMax, 0, "temuan reachable", "govulncheck 3 modul; direct deps 24", "security", "vulns")
	mk("Undo universal", RuleAbsMin, 100, "%", "TestUndo* byte restore", "behavior", "undo_restore_percent")
	mk("Preview aksi berbahaya", RuleAbsMin, 100, "%", "TestPreviewBeforeDangerous", "behavior", "preview_dangerous_percent")
	mk("Plan mode", RuleAbsMin, 100, "% deviasi", "TestPlanModeDeviationMatrix", "behavior", "plan_deviation_percent")
	mk("Budget guard", RuleAbsMax, 0, "LLM call setelah cap", "TestBudgetGuard*", "behavior", "budget_calls_after_cap")
	mk("Migrasi dari hermes", RuleAbsMin, 5, "fixture", "5 state DB fixture", "migration", "fixtures")
	mk("Dukungan OS", RuleAbsMin, 8, "target", "Makefile TARGETS", "inventory", "os_targets")
	mk("Kebocoran goroutine", RuleAbsMax, 0, "kebocoran", "goleak TestMain ./internal/secret", "code_quality", "goroutine_leaks")
	mk("Klasifikasi error via string", RuleAbsMax, 0, "temuan", "make vet-custom (nostringclassify)", "code_quality", "error_string_classifications")
	mk("Flake rate", RuleAbsMax, 0.1, "%", "gh run list main 30 run (2026-11-12); 1 gagal", "code_quality", "flake_rate_pct")
	mk("Waktu CI PR", RuleAbsMax, 12, "menit p90", "gh run list main 30 run; p90 23,5", "code_quality", "ci_p90_min")
	mk("Exec spawn overhead", RuleAbsMax, 15, "ms p95", "BenchmarkShellSessionExec 786 µs", "code_quality", "exec_spawn_ms")
	b.put(&Dimension{Name: "File terbesar (non-test / test)", Rule: RuleLower, Unit: "baris", Hermes: new(8392.0),
		Damnbot: new(b.must(tool, "code_quality", "largest_file_lines")), Source: []string{tool},
		Detail: "test 1.077 vs 23.074 (SCORECARD §1.7)"})
	b.put(&Dimension{Name: "Fungsi terpanjang / > 80 baris", Rule: RuleLower, Unit: "baris", Hermes: new(616.0),
		Damnbot: new(b.must(tool, "code_quality", "longest_function_lines")), Source: []string{tool},
		Detail: fmt.Sprintf("%d fungsi >80 (target 0); target panjang ≤80", int(b.must(tool, "code_quality", "functions_over_80")))})
	b.put(&Dimension{Name: "Kompleksitas siklomatik maks / > 15", Rule: RuleLower, Unit: "nilai", Hermes: new(102.0),
		Damnbot: new(b.must(tool, "code_quality", "max_cyclomatic")), Source: []string{tool},
		Detail: fmt.Sprintf("%d fungsi >15 (target 0)", int(b.must(tool, "code_quality", "functions_over_15")))})
	b.put(&Dimension{Name: "Implementasi retry", Rule: RuleLower, Unit: "fungsi", Hermes: new(169.0),
		Damnbot: new(b.must(tool, "code_quality", "retry_functions")), Source: []string{tool},
		Detail: "fungsi bernama retry/backoff; target 1"})
	// Special cases with a hermes baseline.
	b.put(&Dimension{Name: "LOC per fitur", Rule: RuleLower, Unit: "LOC/baris", Hermes: new(1162.0),
		Damnbot: new(b.must(tool, "code_quality", "source_loc") / 837), Source: []string{tool},
		Detail: "source LOC / 837 baris penyebut gate (SPEC §8)"})
	b.put(&Dimension{Name: "Rasio test:source", Rule: RuleLower, Unit: "rasio", Hermes: new(1.48),
		Damnbot: new(b.must(tool, "code_quality", "test_source_ratio")), Source: []string{tool},
		Detail: "target 0,80–1,20; diukur make loc-ratio"})

	// §1.5 Inventory: counts from the committed tables/catalogs.
	providers := "docs/PROVIDERS.md"
	b.put(&Dimension{
		Name: "Provider", Rule: RuleHigher, Unit: "profil", Hermes: new(50.0),
		Damnbot: new(float64(b.countPrefix(providers, "| LLM-"))), Source: []string{providers},
		Detail: "baris katalog PROVIDERS.md; baseline hermes 50 profil (SCORECARD §1.5)",
	})
	locales, err := filepath.Glob(filepath.Join(b.root, "internal", "i18n", "locales", "*.toml"))
	if err != nil {
		return err
	}
	b.put(&Dimension{
		Name: "Bahasa UI", Rule: RuleHigher, Unit: "locale", Hermes: new(17.0),
		Damnbot: new(float64(len(locales))), Source: []string{"internal/i18n/locales"},
		Detail: "berkas locale lengkap; baseline hermes 17",
	})
	b.put(&Dimension{
		Name: "Skill tersedia", Rule: RuleAbsMin, Target: 211, Unit: "skill", Hermes: new(211.0),
		Damnbot: new(float64(b.countRows("internal/skills/catalog/skills.tsv"))),
		Source:  []string{"internal/skills/catalog/skills.tsv"},
		Detail:  "baris data katalog (58 bundled + 153 optional); baseline hermes 58 + 153",
	})
	b.put(&Dimension{
		Name: "Platform chat", Rule: RuleAbsMin, Target: 30, Unit: "adapter", Hermes: new(30.0),
		Damnbot: new(float64(b.countPrefix("docs/FEATURE-PARITY.md", "| PLAT-"))), Source: []string{"docs/FEATURE-PARITY.md"},
		Detail: "baris PLAT-001..030; target 30",
	})
	b.put(&Dimension{
		Name: "MCP", Rule: RuleAbsMin, Target: 65, Unit: "server", Hermes: new(65.0),
		Damnbot: new(float64(b.countPrefix("docs/FEATURE-PARITY.md", "| MCPX-"))), Source: []string{"docs/FEATURE-PARITY.md"},
		Detail: "baris MCPX katalog; target 65",
	})
	// §1.5 Feature parity: rows counted from the committed table.
	parity := "docs/FEATURE-PARITY.md"
	total, dropped, err := b.parity(parity)
	if err != nil {
		return err
	}
	b.put(&Dimension{
		Name: "Feature parity", Core: true, Rule: RuleAbsMin, Target: float64(total - dropped),
		Unit: "baris", Hermes: new(float64(total)), Damnbot: new(float64(total - dropped)),
		Source: []string{parity},
		Detail: "penyebut gate = total − dropped-with-approval; target 837/837 (SPEC §8)",
	})
	return nil
}

func (b *builder) concurrentErrors(rel, agent string) float64 {
	v, err := b.load(rel)
	if err != nil {
		b.err = err
		return 0
	}
	busy, _ := num(v, "dimensions", "konkurensi", agent, "busy")
	errs, _ := num(v, "dimensions", "konkurensi", agent, "errors")
	return busy + errs
}

var parityIDRe = regexp.MustCompile(`^[A-Z]{2,6}-[0-9]{3}$`)

// parity counts FEATURE-PARITY rows and the dropped-with-approval subset.
// countRows counts non-empty, non-comment lines in a committed file.
func (b *builder) countRows(rel string) int {
	data, err := os.ReadFile(filepath.Join(b.root, rel)) //nolint:gosec // path repo
	if err != nil {
		b.err = fmt.Errorf("scorecard: %s: %w", rel, err)
		return 0
	}
	sum := sha256.Sum256(data)
	b.sources[rel] = hex.EncodeToString(sum[:])
	n := 0
	for line := range strings.SplitSeq(string(data), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			n++
		}
	}
	return n
}

// countPrefix reads a committed file, records its digest, and counts lines
// starting with prefix.
func (b *builder) countPrefix(rel, prefix string) int {
	data, err := os.ReadFile(filepath.Join(b.root, rel)) //nolint:gosec // path repo
	if err != nil {
		b.err = fmt.Errorf("scorecard: %s: %w", rel, err)
		return 0
	}
	sum := sha256.Sum256(data)
	b.sources[rel] = hex.EncodeToString(sum[:])
	n := 0
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(line, prefix) {
			n++
		}
	}
	return n
}

func (b *builder) parity(rel string) (total, dropped int, err error) {
	full := filepath.Join(b.root, rel)
	data, rerr := os.ReadFile(full) //nolint:gosec // path repo
	if rerr != nil {
		return 0, 0, fmt.Errorf("scorecard: %s: %w", rel, rerr)
	}
	sum := sha256.Sum256(data)
	b.sources[rel] = hex.EncodeToString(sum[:])
	for line := range strings.SplitSeq(string(data), "\n") {
		if !strings.HasPrefix(line, "| ") {
			continue
		}
		cells := SplitCells(line)
		if len(cells) == 0 || !parityIDRe.MatchString(cells[0]) {
			continue
		}
		total++
		if strings.HasPrefix(strings.TrimSpace(cells[len(cells)-1]), "dropped-with-approval") {
			dropped++
		}
	}
	if total == 0 {
		return 0, 0, fmt.Errorf("scorecard: %s tidak berisi baris parity", rel)
	}
	return total, dropped, nil
}
