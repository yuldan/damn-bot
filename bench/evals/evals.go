// Package evals builds the locked eval set E1–E10 (SCORECARD §2.2).
//
// Everything under v1/ is generated deterministically from the pinned lists in
// sources/ by Generate; MANIFEST.sha256 locks sources and outputs. Any change
// to a file is a new eval-set version (evalset/v2), never an edit of v1.
package evals

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Version is the eval-set version stored under its own directory.
const Version = "v1"

// Pinned source files (relative to the evals directory).
const (
	SWESource    = "sources/swe-bench-verified.ids"
	TBSource     = "sources/terminal-bench-core-0.1.1.ids"
	SkillsSource = "sources/hermes-skills.tsv"
	ManifestFile = "MANIFEST.sha256"
)

// Task is one eval item; one JSON object per line in <set>.jsonl.
type Task struct {
	ID       string            `json:"id"`
	Set      string            `json:"set"`
	Kind     string            `json:"kind"`
	Source   string            `json:"source"`
	Prompt   string            `json:"prompt,omitempty"`
	Files    map[string]string `json:"files,omitempty"`     // UTF-8 text fixtures
	FilesB64 map[string]string `json:"files_b64,omitempty"` // byte-exact fixtures (BOM, UTF-16)
	Pages    map[string]string `json:"pages,omitempty"`     // local HTTP server: path -> body
	Steps    []Step            `json:"steps,omitempty"`
	Faults   []Fault           `json:"faults,omitempty"`
	Grader   Grader            `json:"grader"`
}

// Step is one scripted user/platform input; Check is a regexp the agent's
// reply to this step must match (empty = no check).
type Step struct {
	Session int    `json:"session"`
	Input   string `json:"input"`
	Check   string `json:"check,omitempty"`
}

// Fault is an injected failure on a harness-controlled resource.
type Fault struct {
	Target string `json:"target"`
	Kind   string `json:"kind"`  // timeout | http_500 | file_locked | malformed_json
	Count  int    `json:"count"` // failing attempts before the resource recovers
}

// Grader is the hidden oracle; the harness never shows it to the agent.
type Grader struct {
	Type         string            `json:"type"`
	Command      string            `json:"command,omitempty"`
	Unchanged    []string          `json:"unchanged,omitempty"` // fixture files that must stay byte-identical
	Reference    map[string]string `json:"reference,omitempty"`
	ReferenceB64 map[string]string `json:"reference_b64,omitempty"`
	Lines        []LineEdit        `json:"lines,omitempty"`
	Pattern      string            `json:"pattern,omitempty"`
	Forbidden    []string          `json:"forbidden,omitempty"`
	Asserts      []Assert          `json:"asserts,omitempty"`
	Label        string            `json:"label,omitempty"`
	TopK         int               `json:"top_k,omitempty"`
}

// LineEdit requires that after the task line N (1-based) of Path must equal Text and all
// other bytes of the file must be unchanged.
type LineEdit struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// Assert is a check on calls recorded by the fake Telegram Bot API (E8).
type Assert struct {
	Method string `json:"method"`
	ChatID int64  `json:"chat_id,omitempty"`
	Match  string `json:"match,omitempty"` // regexp over the call's text/data
	Count  int    `json:"count"`           // exact number of matching calls
}

// Counts are the expected set sizes per SCORECARD §2.2.
var Counts = map[string]int{
	"E1": 160, "E2": 40, "E3": 30, "E4": 30, "E5": 10,
	"E6": 40, "E7": 50, "E8": 20, "E9": 50, "E10": 100,
}

// Sets lists set IDs in manifest order.
var Sets = []string{"E1", "E2", "E3", "E4", "E5", "E6", "E7", "E8", "E9", "E10"}

// Sources are the parsed pinned inputs.
type Sources struct {
	SWE    []string
	TB     []string
	Skills []Skill
}

// Skill is one hermes skill (path, frontmatter name, description).
type Skill struct{ Path, Name, Description string }

// LoadSources reads the pinned lists from the evals directory.
func LoadSources(dir string) (*Sources, error) {
	swe, err := readLines(filepath.Join(dir, SWESource))
	if err != nil {
		return nil, err
	}
	tb, err := readLines(filepath.Join(dir, TBSource))
	if err != nil {
		return nil, err
	}
	rows, err := readLines(filepath.Join(dir, SkillsSource))
	if err != nil {
		return nil, err
	}
	src := &Sources{SWE: swe, TB: tb}
	for _, r := range rows {
		f := strings.Split(r, "\t")
		if len(f) != 3 {
			return nil, fmt.Errorf("%s: bad row %q", SkillsSource, r)
		}
		src.Skills = append(src.Skills, Skill{f[0], f[1], f[2]})
	}
	return src, nil
}

// readLines returns non-empty, non-comment lines.
func readLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("evals: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only file; read errors surface via Scanner.Err
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := sc.Text(); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("evals: %s: %w", path, err)
	}
	return out, nil
}

// Build returns every set's tasks.
func Build(src *Sources) (map[string][]Task, error) {
	internal, err := e1Internal()
	if err != nil {
		return nil, err
	}
	out := map[string][]Task{
		"E1": append(sweTasks(src.SWE, 100), internal...),
		"E2": e2(), "E3": e3(), "E4": e4(), "E5": e5(), "E6": e6(),
		"E7": tbTasks(src.TB, 50), "E8": e8(), "E9": e9(src.Skills, 50), "E10": e10(),
	}
	for _, s := range Sets {
		if len(out[s]) != Counts[s] {
			return nil, fmt.Errorf("evals: %s has %d tasks, want %d", s, len(out[s]), Counts[s])
		}
	}
	return out, nil
}

// Generate writes <dir>/v1/<set>.jsonl and <dir>/MANIFEST.sha256.
func Generate(dir string) error {
	src, err := LoadSources(dir)
	if err != nil {
		return err
	}
	sets, err := Build(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, Version), 0o755); err != nil {
		return fmt.Errorf("evals: %w", err)
	}
	for _, s := range Sets {
		b, encErr := Encode(sets[s])
		if encErr != nil {
			return encErr
		}
		if err := os.WriteFile(filepath.Join(dir, Version, s+".jsonl"), b, 0o644); err != nil {
			return fmt.Errorf("evals: %w", err)
		}
	}
	m, err := Manifest(dir)
	if err != nil {
		return err
	}
	return wrap(os.WriteFile(filepath.Join(dir, ManifestFile), m, 0o644))
}

// Encode renders tasks as JSONL (map keys sorted by encoding/json, no HTML escaping).
func Encode(tasks []Task) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for i := range tasks {
		if err := enc.Encode(&tasks[i]); err != nil {
			return nil, fmt.Errorf("evals: %s: %w", tasks[i].ID, err)
		}
	}
	return buf.Bytes(), nil
}

// ManifestPaths lists the locked files, relative to the evals directory.
func ManifestPaths() []string {
	p := []string{SkillsSource, SWESource, TBSource}
	for _, s := range Sets {
		p = append(p, Version+"/"+s+".jsonl")
	}
	slices.Sort(p)
	return p
}

// Manifest renders "sha256  path\n" lines (sha256sum format) for ManifestPaths.
func Manifest(dir string) ([]byte, error) {
	var b strings.Builder
	b.WriteString("# evalset/" + Version + " — SCORECARD §2.2. Verify: cd bench/evals && sha256sum -c MANIFEST.sha256\n")
	for _, p := range ManifestPaths() {
		data, err := os.ReadFile(filepath.Join(dir, p))
		if err != nil {
			return nil, fmt.Errorf("evals: %w", err)
		}
		sum := sha256.Sum256(data)
		fmt.Fprintf(&b, "%s  %s\n", hex.EncodeToString(sum[:]), p)
	}
	return []byte(b.String()), nil
}

func wrap(err error) error {
	if err != nil {
		return fmt.Errorf("evals: %w", err)
	}
	return nil
}

// pick returns the n IDs with the smallest sha256("evalset/v1|"+id): a
// stable, documented selection that does not depend on list order.
func pick(ids []string, n int) []string {
	type kv struct{ k, id string }
	s := make([]kv, len(ids))
	for i, id := range ids {
		h := sha256.Sum256([]byte("evalset/" + Version + "|" + id))
		s[i] = kv{hex.EncodeToString(h[:]), id}
	}
	slices.SortFunc(s, func(a, b kv) int { return strings.Compare(a.k, b.k) })
	out := make([]string, 0, n)
	for _, x := range s[:n] {
		out = append(out, x.id)
	}
	slices.Sort(out)
	return out
}

func sweTasks(ids []string, n int) []Task {
	var out []Task
	for i, id := range pick(ids, n) {
		out = append(out, Task{
			ID: fmt.Sprintf("E1-%03d", i+1), Set: "E1", Kind: "swe-bench-verified", Source: "swe-bench-verified:" + id,
			Grader: Grader{Type: "swe-bench", Command: "official SWE-bench harness, instance " + id},
		})
	}
	return out
}

func tbTasks(ids []string, n int) []Task {
	var out []Task
	for i, id := range pick(ids, n) {
		out = append(out, Task{
			ID: fmt.Sprintf("E7-%03d", i+1), Set: "E7", Kind: "terminal-bench", Source: "terminal-bench-core@0.1.1:" + id,
			Grader: Grader{Type: "terminal-bench", Command: "tb run --dataset terminal-bench-core==0.1.1 --task-id " + id},
		})
	}
	return out
}

// rng is splitmix64: tiny, and stable across Go releases (math/rand is not
// guaranteed to keep its streams).
type rng uint64

func (r *rng) next() uint64 {
	*r += 0x9e3779b97f4a7c15
	z := uint64(*r)
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

func (r *rng) intn(n int) int { return int(r.next() % uint64(n)) }

func pickOne[T any](r *rng, xs []T) T { return xs[r.intn(len(xs))] }
