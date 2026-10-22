// Command f2measure assembles the F2 baseline artefact (ADR-083): the
// "Konkurensi" and "Token skill per turn" dimensions for Hermes and Damn-Bot,
// measured with the same harness. Concurrency numbers come from
// run.sh (TestConcurrencyF2 + hermes_concurrency.py); token counts use the
// o200k_base tokenizer on the Damn-Bot all-modules tiers and on the Hermes
// request recorded by the h2h proxy. No LLM call is made or needed.
package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/damnbot/damnbot/bench/budget/o200k"
)

type concurrency struct {
	Agent         string `json:"agent"`
	Workers       int    `json:"workers"`
	SharedWorkers int    `json:"shared_workers"`
	Turns         int    `json:"turns"`
	OwnAppends    int    `json:"own_appends"`
	SharedAppends int    `json:"shared_appends"`
	Busy          int    `json:"busy"`
	Errors        int    `json:"errors"`
	Integrity     string `json:"integrity"`
	SQLiteVersion string `json:"sqlite_version,omitempty"`
	JournalMode   string `json:"journal_mode,omitempty"`
	Python        string `json:"python,omitempty"`
}

type tiers struct {
	ContextSlots map[string]string `json:"context_slots"`
	Volatile     string            `json:"volatile"`
}

func main() {
	var (
		damnbotConc = flag.String("damnbot-concurrency", "", "JSON from TestConcurrencyF2")
		hermesConc  = flag.String("hermes-concurrency", "", "JSON from hermes_concurrency.py")
		recording   = flag.String("hermes-recording", "h2h/results/2026-10-18/E1-117.hermes.proxy.jsonl.gz", "hermes proxy recording")
		damnbotTier = flag.String("damnbot-tiers", "../internal/assemble/testdata/p1/all-modules.tiers.json", "all-modules tiers (o200k source)")
		out         = flag.String("out", "f2/results/hermes-868b9f244f-F2.json", "artefact path")
		date        = flag.String("date", time.Now().UTC().Format("2006-01-02"), "measurement date")
		cpu         = flag.String("cpu", "", "CPU model")
		kernel      = flag.String("kernel", "", "kernel")
	)
	flag.Parse()

	tok, err := o200k.Load()
	check(err)
	var dbConc, hConc concurrency
	readJSON(*damnbotConc, &dbConc)
	readJSON(*hermesConc, &hConc)
	var dbTiers tiers
	readJSON(*damnbotTier, &dbTiers)

	hermesIndex, hermesEntries := hermesSkillIndex(*recording)
	hermesSection := hermesSkillSection(*recording)
	dbSkills := tok.Count(dbTiers.ContextSlots["skills"])
	dbVolNames := volatileSkillNames(tok, dbTiers.Volatile)
	ratio := float64(dbSkills) / float64(hermesIndex)

	art := map[string]any{
		"meta": map[string]any{
			"phase":         "F2",
			"date":          *date,
			"hermes_commit": "868b9f244f5aa3ad238e494048046fe4f29c28ba",
			"cpu":           *cpu,
			"kernel":        *kernel,
			"go":            runtime.Version(),
			"tokenizer":     "o200k_base (bench/budget/o200k)",
			"harness":       "bench/f2/run.sh: internal/ctxdb TestConcurrencyF2 + hermes_concurrency.py + f2measure",
			"scenario":      "4 proses x 50 turn (sesi sendiri) + 2 proses x 50 turn (sesi SHARED); 1 turn = 1 user + 1 assistant",
			"llm":           "0 call (tidak perlu LLM untuk kedua dimensi)",
		},
		"dimensions": map[string]any{
			"konkurensi": map[string]any{
				"metric":  "4 proses x 50 turn + 2 proses berebut sesi; error SQLITE_BUSY/korupsi",
				"target":  "0 SQLITE_BUSY, 0 error lain, integritas ok",
				"damnbot": dbConc,
				"hermes":  hConc,
				"verdict": verdictConc(dbConc) && verdictConc(hConc),
			},
			"token_skill_per_turn": map[string]any{
				"metric": "token index/isi skill di request (o200k_base)",
				"target": "<= 150 tok context + <= 20 tok volatile; <= 0,2x hermes",
				"damnbot": map[string]any{
					"context_skills":       dbSkills,
					"volatile_skill_names": dbVolNames,
					"corpus":               "211 skill terpasang (all-modules), retrieval top-3",
					"source":               "internal/assemble/testdata/p1/all-modules.tiers.json (context_slots.skills, volatile new: line)",
					"upstream_all_skills":  0,
					"note":                 "Damn-Bot hanya mengirim top-3 (CapSkills 150) + nama skill baru; katalog penuh tidak masuk prompt",
				},
				"hermes": map[string]any{
					"available_skills_tokens":  hermesIndex,
					"available_skills_entries": hermesEntries,
					"skills_section_tokens":    hermesSection,
					"corpus":                   "katalog skill bawaan hermes (seluruh entri di system prompt)",
					"source":                   "h2h proxy recording E1-117 (POST /chat/completions)",
				},
				"ratio_damnbot_vs_hermes": ratio,
				"verdict":                 dbSkills <= 150 && dbVolNames <= 20 && ratio <= 0.2,
			},
		},
	}
	raw, err := json.MarshalIndent(art, "", " ")
	check(err)
	check(os.MkdirAll(filepath.Dir(*out), 0o755))
	check(os.WriteFile(*out, append(raw, '\n'), 0o644))
	fmt.Printf("wrote %s (damnbot skills %d, hermes index %d, ratio %.3f)\n", *out, dbSkills, hermesIndex, ratio)
}

func verdictConc(c concurrency) bool {
	return c.Busy == 0 && c.Errors == 0 && c.Integrity == "ok" &&
		c.Workers == 4 && c.SharedWorkers == 2 && c.Turns == 50 &&
		c.OwnAppends == 400 && c.SharedAppends == 200
}

// hermesSkillIndex extracts the <available_skills> block from the first
// recorded chat request and counts its o200k tokens and entries.
func hermesSkillIndex(path string) (int, int) {
	sys := hermesSystem(path)
	start := strings.Index(sys, "<available_skills>")
	end := strings.Index(sys, "</available_skills>")
	if start < 0 || end < start {
		return 0, 0
	}
	block := sys[start : end+len("</available_skills>")]
	tok, err := o200k.Load()
	check(err)
	entries := 0
	for line := range strings.SplitSeq(block, "\n") {
		if strings.HasPrefix(line, "  ") && strings.Contains(line, ": ") {
			entries++
		}
	}
	return tok.Count(block), entries
}

// hermesSkillSection counts the whole "## Skills" section (instructions plus
// the available_skills block) for transparency; the compared metric is the
// block above.
func hermesSkillSection(path string) int {
	sys := hermesSystem(path)
	lines := strings.Split(sys, "\n")
	start := slices.Index(lines, "## Skills")
	if start < 0 {
		return 0
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "Only proceed without loading a skill") {
			end = i
			break
		}
	}
	tok, err := o200k.Load()
	check(err)
	return tok.Count(strings.Join(lines[start:end], "\n"))
}

// hermesSystem returns the system message of the first recorded chat request.
func hermesSystem(path string) string {
	f, err := os.Open(path) //nolint:gosec // repo-local recording
	check(err)
	defer func() { check(f.Close()) }()
	zr, err := gzip.NewReader(f)
	check(err)
	defer func() { check(zr.Close()) }()
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		var rec struct {
			Method  string          `json:"method"`
			Path    string          `json:"path"`
			ReqBody json.RawMessage `json:"req_body"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil || rec.Method != "POST" ||
			!strings.HasSuffix(rec.Path, "/chat/completions") {
			continue
		}
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal(rec.ReqBody, &body) != nil {
			continue
		}
		for _, m := range body.Messages {
			// OpenAI-compatible providers use both "system" and "developer"
			// for the instruction message; hermes 868b9f244f sends the skill
			// catalog in a developer message.
			if (m.Role == "system" || m.Role == "developer") && strings.Contains(m.Content, "<available_skills>") {
				return m.Content
			}
		}
	}
	check(sc.Err())
	panic("f2measure: no recorded request with <available_skills>")
}

var skillRef = regexp.MustCompile(`skill://[a-z0-9_-]+`)

// volatileSkillNames counts the o200k tokens of the skill names in the
// volatile "new:" line (the only volatile skill component).
func volatileSkillNames(tok *o200k.Tokenizer, volatile string) int {
	for line := range strings.SplitSeq(volatile, "\n") {
		if strings.HasPrefix(line, "new: ") {
			return tok.Count(strings.Join(skillRef.FindAllString(line, -1), ", "))
		}
	}
	return 0
}

func readJSON(path string, v any) {
	raw, err := os.ReadFile(path) //nolint:gosec // caller-selected input
	check(err)
	check(json.Unmarshal(raw, v))
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
