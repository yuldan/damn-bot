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
const ArtefactPath = "scorecard/results/2026-11-12-aeb4dc65.json"

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
	f3d := "bench/f3/results/damnbot-6d01e7a1d5-F3.json"
	f3h := "bench/f3/results/hermes-868b9f244f-F3.json"
	b.put(&Dimension{
		Name: "Startup", Core: true, Rule: RuleLowerMax, MaxRatio: 0.1, Unit: "ms p50",
		Hermes:  new(b.must(f3h, "dimensions", "tui", "first_frame_p50_ms")),
		Damnbot: new(b.must(f3d, "dimensions", "startup_p12", "p50_ms")),
		Source:  []string{f3d, f3h},
		Detail:  "frame pertama TUI; target 10x (SPEC P12/F3)",
	})
	b.put(&Dimension{
		Name: "First token di UI", Core: true, Rule: RuleLowerMax, MaxRatio: 0.2, Unit: "ms p50",
		Hermes:  new(b.must(f3h, "dimensions", "tui", "first_token", "p50_ms")),
		Damnbot: new(b.must(f3d, "dimensions", "first_token_tui", "p50_ms")),
		Source:  []string{f3d, f3h},
		Detail:  "T0 fake provider → token terlihat di TUI",
	})
	b.put(&Dimension{
		Name: "Waktu build bersih / inkremental", Rule: RuleLower, Unit: "detik p50 (clean)",
		Hermes:  new(b.must(f3h, "dimensions", "buildtime", "clean", "p50_s")),
		Damnbot: new(b.must(f3d, "dimensions", "buildtime", "clean", "p50_s")),
		Source:  []string{f3d, f3h},
		Detail: fmt.Sprintf("inkremental: hermes %.3f s vs damnbot %.3f s",
			b.must(f3h, "dimensions", "buildtime", "incremental", "p50_s"),
			b.must(f3d, "dimensions", "buildtime", "incremental", "p50_s")),
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
