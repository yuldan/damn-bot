package evals

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestManifest: MANIFEST.sha256 lists exactly the locked files and every
// hash matches (equivalent to `sha256sum -c`).
func TestManifest(t *testing.T) {
	t.Parallel()
	f, err := os.Open(ManifestFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // read-only file
	var got []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		sum, path, ok := strings.Cut(line, "  ")
		if !ok {
			t.Fatalf("bad manifest line %q", line)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(data)
		if hex.EncodeToString(h[:]) != sum {
			t.Errorf("%s: sha256 mismatch (eval set changed: bump to a new version instead)", path)
		}
		got = append(got, path)
	}
	if want := ManifestPaths(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("manifest paths %v, want %v", got, want)
	}
}

// TestRegenerate: the committed sets are byte-identical to a fresh build
// from the pinned sources (the generator is deterministic).
func TestRegenerate(t *testing.T) {
	t.Parallel()
	src, err := LoadSources(".")
	if err != nil {
		t.Fatal(err)
	}
	sets, err := Build(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range Sets {
		b, err := Encode(sets[s])
		if err != nil {
			t.Fatal(err)
		}
		disk, err := os.ReadFile(filepath.Join(Version, s+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b, disk) {
			t.Errorf("%s: committed file differs from generator output", s)
		}
	}
}

// TestSources: pinned lists have the published sizes and no duplicates.
func TestSources(t *testing.T) {
	t.Parallel()
	src, err := LoadSources(".")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(src.Skills))
	for i, s := range src.Skills {
		names[i] = s.Name
	}
	for _, c := range []struct {
		name string
		ids  []string
		want int
	}{{"SWE-bench Verified", src.SWE, 500}, {"Terminal-Bench core 0.1.1", src.TB, 80}, {"hermes skills", names, 211}} {
		if len(c.ids) != c.want || len(set(c.ids)) != c.want {
			t.Errorf("%s: %d entries (%d unique), want %d", c.name, len(c.ids), len(set(c.ids)), c.want)
		}
	}
}

func set(xs []string) map[string]bool {
	m := map[string]bool{}
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func load(t *testing.T, s string) []Task {
	t.Helper()
	f, err := os.Open(filepath.Join(Version, s+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // read-only file
	var out []Task
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	for dec.More() {
		var x Task
		if err := dec.Decode(&x); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		out = append(out, x)
	}
	return out
}

// kinds counts tasks per Kind prefix (text before ':').
func kinds(ts []Task) map[string]int {
	m := map[string]int{}
	for _, x := range ts {
		k, _, _ := strings.Cut(x.Kind, ":")
		m[k]++
	}
	return m
}

// TestCountsPerScorecard checks set sizes and composition per SCORECARD §2.2.
func TestCountsPerScorecard(t *testing.T) {
	t.Parallel()
	src, err := LoadSources(".")
	if err != nil {
		t.Fatal(err)
	}
	all := map[string][]Task{}
	for _, s := range Sets {
		all[s] = load(t, s)
		if len(all[s]) != Counts[s] {
			t.Errorf("%s: %d tasks, want %d", s, len(all[s]), Counts[s])
		}
		ids := map[string]bool{}
		for _, x := range all[s] {
			if x.Set != s || !strings.HasPrefix(x.ID, s+"-") || ids[x.ID] {
				t.Errorf("%s: bad or duplicate id %q (set %q)", s, x.ID, x.Set)
			}
			ids[x.ID] = true
		}
	}
	want := map[string]map[string]int{
		"E1": {
			"swe-bench-verified": 100, "internal-go": 10, "internal-python": 10, "internal-typescript": 10,
			"internal-rust": 10, "internal-java": 10, "internal-shell": 10,
		},
		"E2":  {"rename": 10, "crlf": 8, "bom": 7, "utf16": 5, "large": 10},
		"E3":  {"founded": 10, "elevation": 10, "population": 10},
		"E4":  {"recall-n+1": 7, "recall-n+3": 7, "recall-n+10": 6, "correction-n+1": 3, "correction-n+3": 3, "correction-n+10": 4},
		"E5":  {"inventory-120": 10},
		"E6":  {"timeout": 10, "http_500": 10, "file_locked": 10, "malformed_json": 10},
		"E7":  {"terminal-bench": 50},
		"E8":  {"allowlist": 5, "pairing": 5, "approval": 5, "restart": 5},
		"E9":  {"route": 50},
		"E10": {"web": 20, "file": 20, "mcp": 20, "chat": 20, "skill": 20},
	}
	for s, w := range want {
		got := kinds(all[s])
		for k, n := range w {
			if got[k] != n {
				t.Errorf("%s kind %s: %d, want %d", s, k, got[k], n)
			}
		}
		if len(got) != len(w) {
			t.Errorf("%s: kinds %v, want %v", s, got, w)
		}
	}
	checkPinned(t, all["E1"], "swe-bench-verified:", set(src.SWE))
	checkPinned(t, all["E7"], "terminal-bench-core@0.1.1:", set(src.TB))
	checkE5(t, all["E5"])
	checkE9(t, all["E9"], src.Skills)
	for _, s := range Sets {
		checkRegexps(t, all[s])
	}
}

func checkPinned(t *testing.T, ts []Task, prefix string, pinned map[string]bool) {
	t.Helper()
	seen := map[string]bool{}
	for _, x := range ts {
		if id, ok := strings.CutPrefix(x.Source, prefix); ok {
			if !pinned[id] || seen[id] {
				t.Errorf("%s: %q not in pinned list or duplicated", x.ID, id)
			}
			seen[id] = true
		}
	}
}

// checkE5: 120 turns per session, a checkpoint every 20 turns.
func checkE5(t *testing.T, ts []Task) {
	t.Helper()
	for _, x := range ts {
		if len(x.Steps) != 120 {
			t.Errorf("%s: %d turns, want 120", x.ID, len(x.Steps))
		}
		for i, st := range x.Steps {
			if (st.Check != "") != ((i+1)%20 == 0) {
				t.Errorf("%s turn %d: checkpoint placement", x.ID, i+1)
			}
		}
	}
}

// checkE9: 50 distinct labels, each an existing hermes skill.
func checkE9(t *testing.T, ts []Task, skills []Skill) {
	t.Helper()
	names := map[string]bool{}
	for _, s := range skills {
		names[s.Name] = true
	}
	labels := map[string]bool{}
	for _, x := range ts {
		if !names[x.Grader.Label] || labels[x.Grader.Label] || x.Grader.TopK != 3 {
			t.Errorf("%s: label %q", x.ID, x.Grader.Label)
		}
		labels[x.Grader.Label] = true
	}
}

// checkRegexps: every oracle regexp compiles once placeholders are filled.
func checkRegexps(t *testing.T, ts []Task) {
	t.Helper()
	fill := strings.NewReplacer(exfilURL, regexp.QuoteMeta("http://127.0.0.1:9"), baseURL, regexp.QuoteMeta("http://127.0.0.1:8"))
	for _, x := range ts {
		res := []string{x.Grader.Pattern}
		for _, st := range x.Steps {
			res = append(res, st.Check)
		}
		for _, a := range x.Grader.Asserts {
			res = append(res, a.Match)
		}
		for _, f := range x.Grader.Forbidden {
			ch, re, ok := strings.Cut(f, ":")
			if !ok || ch == "" {
				t.Errorf("%s: forbidden %q lacks a channel", x.ID, f)
			}
			res = append(res, re)
		}
		for _, r := range res {
			if _, err := regexp.Compile(fill.Replace(r)); err != nil {
				t.Errorf("%s: %v", x.ID, err)
			}
		}
	}
}
