package phase

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestEdgeCaseCatalogSync is the cross-check the roadmap names (ADR-077,
// ADR-080): the edge-case catalogs that call Gate.Item must cover exactly
// the EC ids the roadmap requires, at the phase the roadmap assigns.
//
// Without it the incremental gate can only see items a catalog chose to
// declare; an EC id that no package catalog mentions is invisible and a
// phase acceptance run passes while the item is missing. Phase labels are
// checked on every run. In acceptance mode the check is phase-relative,
// matching Gate.Item: ids with a phase above PHASE may be absent, ids at
// or below PHASE must be cataloged and covered or n/a. Normal test runs
// stay green and log the uncataloged ids.
func TestEdgeCaseCatalogSync(t *testing.T) {
	t.Parallel()
	g := Current(t)
	root := moduleRoot(t)
	required := parseRoadmap(t, root)
	catalog := scanCatalogs(t, root)

	// Phase labels are always checked: a catalog that claims the wrong phase
	// would be invisible to Gate.Item at the wrong time.
	t.Run("PhaseMatchesRoadmap", func(t *testing.T) {
		for id, e := range catalog {
			want, ok := required[id]
			if !ok {
				t.Errorf("PHASE GAGAL %s: cataloged in %s but absent from docs/ROADMAP.md", id, e.file)
				continue
			}
			if e.phase != 0 && e.phase != want {
				t.Errorf("PHASE GAGAL %s: catalog phase F%d, roadmap phase F%d (%s)", id, e.phase, want, e.file)
			}
		}
	})

	if !g.acceptance {
		if missing := uncataloged(required, catalog, g.cur); len(missing) > 0 {
			t.Logf("PHASE acceptance only: %d EC ids at or below F%d have no catalog row: %s",
				len(missing), g.cur, strings.Join(missing, " "))
		}
		return
	}

	// Acceptance for the phase being entered: every item with a roadmap phase
	// at or below PHASE must be cataloged and covered. The old gate could only
	// see declared rows, so an id no catalog mentioned passed silently.
	t.Run("RequiredIDsCataloged", func(t *testing.T) {
		for _, id := range uncataloged(required, catalog, g.cur) {
			t.Errorf("PHASE GAGAL %s: required at F%d but no package catalog declares it", id, required[id])
		}
	})
	t.Run("CoveredAtPhase", func(t *testing.T) {
		for id, p := range required {
			if p > g.cur {
				continue
			}
			e, ok := catalog[id]
			if !ok {
				continue // RequiredIDsCataloged reports it
			}
			if e.status != "covered" && e.status != "n/a" {
				t.Errorf("PHASE GAGAL %s: phase acceptance F%d: catalog status %q (%s)", id, g.cur, e.status, e.file)
			}
		}
	})
}

// uncataloged lists required ids at or below phase cur with no catalog row.
func uncataloged(required map[string]int, catalog map[string]catalogEntry, cur int) []string {
	var out []string
	for id, p := range required {
		if _, ok := catalog[id]; p <= cur && !ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// catalogEntry is one EC row found in a package catalog.
type catalogEntry struct {
	phase  int
	status string
	file   string
}

// moduleRoot walks up from the package directory to the go.mod root.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("PHASE GAGAL: getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("PHASE GAGAL: go.mod not found above the working directory")
		}
		dir = parent
	}
}

var (
	roadmapArea  = regexp.MustCompile("^\\| `(EC-([A-Z]+))`")
	roadmapTok   = regexp.MustCompile(`EC-[A-Z]+-\d{3}|F[1-9]|\d{3}(?:\.\.\d{3})?`)
	edgeCaseID   = regexp.MustCompile(`\| (EC-[A-Z]+-\d{3}) \|`)
	itemCall     = regexp.MustCompile(`\.Item\([^,]+,[^,]+,\s*"(F[1-9])"\s*,`)
	quotedEC     = regexp.MustCompile(`"(EC-[A-Z]+-\d{3})"`)
	rowStatus    = regexp.MustCompile(`"(EC-[A-Z]+-\d{3})"\s*,\s*"(pending:F[1-9]|n/a)"`)
	roadmapNoise = regexp.MustCompile(`ADR-\d+|#\d+|\(\d+\)`)
)

// parseRoadmap returns the phase the ROADMAP EC table assigns to every id,
// using docs/EDGE-CASES-hermes.md as the id universe for "all ids" rows.
func parseRoadmap(t *testing.T, root string) map[string]int {
	t.Helper()
	roadmap := readDoc(t, root, "docs/ROADMAP.md")
	edge := readDoc(t, root, "docs/EDGE-CASES-hermes.md")

	areaIDs := make(map[string][]string)
	for _, m := range edgeCaseID.FindAllStringSubmatch(edge, -1) {
		area := m[1][:strings.LastIndex(m[1], "-")]
		areaIDs[area] = appendUnique(areaIDs[area], m[1])
	}

	out := make(map[string]int)
	for _, line := range strings.Split(roadmap, "\n") {
		m := roadmapArea.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		area, ids := m[1], areaIDs[m[1]]
		if len(ids) == 0 {
			t.Fatalf("PHASE GAGAL %s: no ids in docs/EDGE-CASES-hermes.md", area)
		}
		cells := strings.Split(line, "|")
		if len(cells) < 4 {
			t.Fatalf("PHASE GAGAL %s: malformed roadmap row %q", area, line)
		}
		spec := cells[len(cells)-2]
		if i := strings.Index(spec, ". "); i >= 0 {
			spec = spec[:i] // drop prose after the id segments
		}
		spec = roadmapNoise.ReplaceAllString(spec, "")

		type seg struct {
			phase int
			ids   []string
		}
		var segs []seg
		for _, tok := range roadmapTok.FindAllString(spec, -1) {
			if strings.HasPrefix(tok, "F") {
				p, err := strconv.Atoi(tok[1:])
				if err != nil {
					t.Fatalf("PHASE GAGAL %s: phase %q: %v", area, tok, err)
				}
				segs = append(segs, seg{phase: p})
				continue
			}
			if len(segs) == 0 {
				t.Fatalf("PHASE GAGAL %s: id token %q before a phase in %q", area, tok, spec)
			}
			segs[len(segs)-1].ids = append(segs[len(segs)-1].ids, expandAfter(tok, area)...)
		}
		assigned := make(map[string]bool)
		for _, s := range segs {
			for _, id := range s.ids {
				if assigned[id] {
					t.Fatalf("PHASE GAGAL %s: id %s assigned twice in %q", area, id, spec)
				}
				assigned[id] = true
				if prev, ok := out[id]; ok && prev != s.phase {
					t.Fatalf("PHASE GAGAL %s: phase F%d then F%d", id, prev, s.phase)
				}
				out[id] = s.phase
			}
		}
		for _, s := range segs {
			if len(s.ids) > 0 {
				continue
			}
			for _, id := range ids { // bare "F2" / "(Telegram)" segment: the rest
				if !assigned[id] {
					assigned[id] = true
					out[id] = s.phase
				}
			}
		}
		for _, id := range ids {
			if !assigned[id] {
				t.Fatalf("PHASE GAGAL %s: %s has no phase in %q", area, id, spec)
			}
		}
	}
	if len(out) == 0 {
		t.Fatal("PHASE GAGAL: no EC ids parsed from docs/ROADMAP.md")
	}
	return out
}

// expandAfter turns a bare number, a range or a full id into EC ids of the
// given area.
func expandAfter(tok, area string) []string {
	if strings.HasPrefix(tok, "EC-") {
		return []string{tok}
	}
	if lo, hi, ok := strings.Cut(tok, ".."); ok {
		from, _ := strconv.Atoi(lo)
		to, _ := strconv.Atoi(hi)
		var out []string
		for n := from; n <= to; n++ {
			out = append(out, area+"-"+pad(n))
		}
		return out
	}
	n, _ := strconv.Atoi(tok)
	return []string{area + "-" + pad(n)}
}

func pad(n int) string { return fmt.Sprintf("%03d", n) }

func appendUnique(xs []string, x string) []string {
	for _, v := range xs {
		if v == x {
			return xs
		}
	}
	return append(xs, x)
}

// scanCatalogs collects every EC id named by a test file. A file that
// calls Gate.Item also supplies the catalog phase (the call's third
// argument; one phase per file) and may mark a row pending or n/a. Files
// without a gate contribute presence only (phase 0): they run
// unconditionally, so their ids count as covered.
func scanCatalogs(t *testing.T, root string) map[string]catalogEntry {
	t.Helper()
	out := make(map[string]catalogEntry)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == ".git" || name == "testdata" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path) //nolint:gosec // repo-local test walk
		if err != nil {
			return err
		}
		body := string(raw)
		phases := make(map[int]bool)
		for _, m := range itemCall.FindAllStringSubmatch(body, -1) {
			p, err := strconv.Atoi(m[1][1:])
			if err != nil {
				t.Fatalf("PHASE GAGAL %s: phase %q: %v", path, m[1], err)
			}
			phases[p] = true
		}
		rel, _ := filepath.Rel(root, path)
		if len(phases) > 1 {
			t.Errorf("PHASE GAGAL %s: catalog mixes phases %v", rel, phases)
			return nil
		}
		p := 0 // untagged file: presence only, phase is not asserted
		for ph := range phases {
			p = ph
		}
		status := make(map[string]string)
		for _, m := range rowStatus.FindAllStringSubmatch(body, -1) {
			status[m[1]] = m[2]
		}
		for _, m := range quotedEC.FindAllStringSubmatch(body, -1) {
			id := m[1]
			st, ok := status[id]
			if !ok {
				st = "covered"
			}
			if prev, ok := out[id]; ok {
				if prev.phase != 0 && p != 0 && prev.phase != p {
					t.Errorf("PHASE GAGAL %s: cataloged at F%d (%s) and F%d (%s)", id, prev.phase, prev.file, p, rel)
					continue
				}
				if prev.phase != 0 {
					continue // a gated row carries the phase
				}
			}
			out[id] = catalogEntry{phase: p, status: st, file: rel}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("PHASE GAGAL: walk: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("PHASE GAGAL: no EC catalogs found")
	}
	return out
}

func readDoc(t *testing.T, root, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // repo-local docs
	if err != nil {
		t.Fatalf("PHASE GAGAL: %v", err)
	}
	return string(raw)
}
