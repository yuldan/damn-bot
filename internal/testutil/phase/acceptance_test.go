package phase

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
// checked on every run.
//
// Acceptance mode is phase-relative and certifies the phase being left
// (ADR-080 butir 2, ROADMAP §"Regression suite", ADR-176): the job runs while
// PHASE is raised from Fx to Fx+1, so every id at or below PHASE-1 must be
// cataloged and covered or n/a, while an id of the phase being entered
// (phase = PHASE) may still be pending:<PHASE> and may even be absent. At
// PHASE = F9 there is no next phase, so no catalog row may stay pending. Ids
// above PHASE are only logged. Normal test runs stay green and log the
// uncataloged ids.
func TestEdgeCaseCatalogSync(t *testing.T) {
	t.Parallel()
	g := Current(t)
	root := moduleRoot(t)
	required := parseRoadmap(t, root)
	catalog := scanCatalogs(t, root)

	// Phase labels are always checked: a catalog that claims the wrong phase
	// would be invisible to Gate.Item at the wrong time.
	t.Run("PhaseMatchesRoadmap", func(t *testing.T) { checkPhaseMatches(t, required, catalog) })

	if !g.acceptance {
		logUncataloged(t, required, catalog, g.cur)
		return
	}
	// Acceptance certifies the phase being left: the job runs while PHASE is
	// raised from Fx to Fx+1, so Fx = PHASE-1 is the highest phase it may
	// require (ADR-080 butir 2, ADR-176). The phase being entered keeps its
	// right to be pending; everything above it is only logged.
	t.Run("RequiredIDsCataloged", func(t *testing.T) { checkRequiredIDs(t, required, catalog, g.cur) })
	t.Run("CoveredAtPhase", func(t *testing.T) { checkCovered(t, required, catalog, g.cur) })
	logOpen(t, required, catalog, g.cur)
}

// requiredPhase is the highest phase the acceptance run requires to be
// cataloged and covered: PHASE-1 (ADR-176).
func requiredPhase(cur int) int { return cur - 1 }

// phaseMax is the last phase PHASE may hold (F9).
const phaseMax = 9

func phaseName(p int) string { return fmt.Sprintf("F%d", p) }

// violation is one acceptance violation: the id and the reason, without the
// "PHASE GAGAL" prefix.
type violation struct{ id, why string }

func checkPhaseMatches(t *testing.T, required map[string]int, catalog map[string]catalogEntry) {
	t.Helper()
	for id, e := range catalog {
		want, ok := required[id]
		switch {
		case !ok:
			t.Errorf("PHASE GAGAL %s: cataloged in %s but absent from docs/ROADMAP.md", id, e.file)
		case e.phase != 0 && e.phase != want:
			t.Errorf("PHASE GAGAL %s: catalog phase F%d, roadmap phase F%d (%s)", id, e.phase, want, e.file)
		}
	}
}

func logUncataloged(t *testing.T, required map[string]int, catalog map[string]catalogEntry, cur int) {
	t.Helper()
	if missing := uncataloged(required, catalog, cur); len(missing) > 0 {
		t.Logf("PHASE acceptance only: %d EC ids at or below F%d have no catalog row: %s",
			len(missing), cur, strings.Join(missing, " "))
	}
}

// logOpen reports the ids at or above PHASE with no catalog row or a pending
// one. They are not acceptance failures — the next bump requires them — but
// the log is where the report shows what is still open.
func logOpen(t *testing.T, required map[string]int, catalog map[string]catalogEntry, cur int) {
	t.Helper()
	var open []string
	for id, p := range required {
		if p < cur {
			continue
		}
		e, ok := catalog[id]
		switch {
		case !ok:
			open = append(open, fmt.Sprintf("%s/F%d:no-row", id, p))
		case strings.HasPrefix(e.status, "pending:"):
			open = append(open, fmt.Sprintf("%s/F%d:%s", id, p, e.status))
		}
	}
	if len(open) == 0 {
		return
	}
	sort.Strings(open)
	t.Logf("PHASE acceptance F%d: %d EC ids of a later phase are still open: %s",
		cur, len(open), strings.Join(open, " "))
}

func checkRequiredIDs(t *testing.T, required map[string]int, catalog map[string]catalogEntry, cur int) {
	t.Helper()
	for _, v := range uncatalogedViolations(required, catalog, cur) {
		t.Errorf("PHASE GAGAL %s: %s", v.id, v.why)
	}
}

func checkCovered(t *testing.T, required map[string]int, catalog map[string]catalogEntry, cur int) {
	t.Helper()
	for _, v := range coverageViolations(required, catalog, cur) {
		t.Errorf("PHASE GAGAL %s: %s", v.id, v.why)
	}
}

// uncatalogedViolations lists every id at or below PHASE-1 that has no catalog
// row at all.
func uncatalogedViolations(required map[string]int, catalog map[string]catalogEntry, cur int) []violation {
	var out []violation
	for _, id := range uncataloged(required, catalog, requiredPhase(cur)) {
		out = append(out, violation{id, fmt.Sprintf("required at F%d but no package catalog declares it", required[id])})
	}
	return out
}

// coverageViolations lists every catalog row that breaks the acceptance rule:
// an id at or below PHASE-1 must be covered or n/a, a row of the phase being
// entered may be covered, n/a or pending:<PHASE> (its own label) and may be
// absent, and at PHASE = F9 nothing may be pending at all.
func coverageViolations(required map[string]int, catalog map[string]catalogEntry, cur int) []violation {
	var out []violation
	for id, p := range required {
		if p > requiredPhase(cur) {
			continue
		}
		e, ok := catalog[id]
		if !ok {
			continue // uncatalogedViolations reports it
		}
		if e.status != "covered" && e.status != "n/a" {
			out = append(out, violation{id, fmt.Sprintf("phase acceptance F%d: catalog status %q (%s)", cur, e.status, e.file)})
		}
	}
	for id, e := range catalog {
		if required[id] != cur {
			continue
		}
		if e.status == "covered" || e.status == "n/a" || e.status == "pending:"+phaseName(cur) {
			continue
		}
		out = append(out, violation{id, fmt.Sprintf("phase acceptance F%d: catalog status %q (%s), want covered, n/a or pending:%s",
			cur, e.status, e.file, phaseName(cur))})
	}
	if cur == phaseMax {
		for id, e := range catalog {
			if strings.HasPrefix(e.status, "pending:") {
				out = append(out, violation{id, fmt.Sprintf("phase acceptance F9: item is still pending (%s)", e.file)})
			}
		}
	}
	slices.SortFunc(out, func(a, b violation) int {
		if c := strings.Compare(a.id, b.id); c != 0 {
			return c
		}
		return strings.Compare(a.why, b.why)
	})
	return out
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
	areaIDs := edgeAreas(readDoc(t, root, "docs/EDGE-CASES-hermes.md"))
	out := make(map[string]int)
	for line := range strings.SplitSeq(roadmap, "\n") {
		m := roadmapArea.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		assignArea(t, m[1], line, areaIDs[m[1]], out)
	}
	if len(out) == 0 {
		t.Fatal("PHASE GAGAL: no EC ids parsed from docs/ROADMAP.md")
	}
	return out
}

// edgeAreas collects the id universe per EC area.
func edgeAreas(edge string) map[string][]string {
	areaIDs := make(map[string][]string)
	for _, m := range edgeCaseID.FindAllStringSubmatch(edge, -1) {
		area := m[1][:strings.LastIndex(m[1], "-")]
		areaIDs[area] = appendUnique(areaIDs[area], m[1])
	}
	return areaIDs
}

// phaseSeg is one "F<n>: ids" segment of a roadmap cell.
type phaseSeg struct {
	phase int
	ids   []string
}

// assignArea parses one roadmap row and writes its ids into out.
func assignArea(t *testing.T, area, line string, ids []string, out map[string]int) {
	t.Helper()
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
	segs := parseSegments(t, area, spec)
	assigned := assignSegments(t, area, spec, segs, out)
	fillBareSegments(segs, ids, assigned, out)
	for _, id := range ids {
		if !assigned[id] {
			t.Fatalf("PHASE GAGAL %s: %s has no phase in %q", area, id, spec)
		}
	}
}

// assignSegments writes every explicit id of segs into out and returns the
// assigned set, rejecting duplicates and phase conflicts.
func assignSegments(t *testing.T, area, spec string, segs []phaseSeg, out map[string]int) map[string]bool {
	t.Helper()
	assigned := make(map[string]bool)
	for _, seg := range segs {
		for _, id := range seg.ids {
			if assigned[id] {
				t.Fatalf("PHASE GAGAL %s: id %s assigned twice in %q", area, id, spec)
			}
			assigned[id] = true
			if prev, ok := out[id]; ok && prev != seg.phase {
				t.Fatalf("PHASE GAGAL %s: phase F%d then F%d", id, prev, seg.phase)
			}
			out[id] = seg.phase
		}
	}
	return assigned
}

// fillBareSegments gives a segment without ids (bare "F2" or "(Telegram)")
// every area id not assigned to another segment.
func fillBareSegments(segs []phaseSeg, ids []string, assigned map[string]bool, out map[string]int) {
	for _, seg := range segs {
		if len(seg.ids) > 0 {
			continue
		}
		for _, id := range ids {
			if !assigned[id] {
				assigned[id] = true
				out[id] = seg.phase
			}
		}
	}
}

// parseSegments tokenizes the phase cell into phase segments.
func parseSegments(t *testing.T, area, spec string) []phaseSeg {
	t.Helper()
	var segs []phaseSeg
	for _, tok := range roadmapTok.FindAllString(spec, -1) {
		if strings.HasPrefix(tok, "F") {
			p, err := strconv.Atoi(tok[1:])
			if err != nil {
				t.Fatalf("PHASE GAGAL %s: phase %q: %v", area, tok, err)
			}
			segs = append(segs, phaseSeg{phase: p})
			continue
		}
		if len(segs) == 0 {
			t.Fatalf("PHASE GAGAL %s: id token %q before a phase in %q", area, tok, spec)
		}
		last := &segs[len(segs)-1]
		last.ids = append(last.ids, expandAfter(t, tok, area)...)
	}
	return segs
}

// expandAfter turns a bare number, a range or a full id into EC ids of the
// given area.
func expandAfter(t *testing.T, tok, area string) []string {
	t.Helper()
	if strings.HasPrefix(tok, "EC-") {
		return []string{tok}
	}
	parse := func(s string) int {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("PHASE GAGAL %s: number %q: %v", area, s, err)
		}
		return n
	}
	if lo, hi, ok := strings.Cut(tok, ".."); ok {
		from, to := parse(lo), parse(hi)
		var out []string
		for n := from; n <= to; n++ {
			out = append(out, area+"-"+pad(n))
		}
		return out
	}
	return []string{area + "-" + pad(parse(tok))}
}

func pad(n int) string { return fmt.Sprintf("%03d", n) }

func appendUnique(xs []string, x string) []string {
	if slices.Contains(xs, x) {
		return xs
	}
	return append(xs, x)
}

// scanCatalogs collects every EC id named by a test file. A file that calls
// Gate.Item also supplies the catalog phase (the call's third argument; one
// phase per file) and may mark a row pending or n/a. Files without a gate
// contribute presence only (phase 0): they run unconditionally, so their ids
// count as covered.
func scanCatalogs(t *testing.T, root string) map[string]catalogEntry {
	t.Helper()
	out := make(map[string]catalogEntry)
	if err := walkCatalogs(t, root, out); err != nil {
		t.Fatalf("PHASE GAGAL: walk: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("PHASE GAGAL: no EC catalogs found")
	}
	return out
}

func walkCatalogs(t *testing.T, root string, out map[string]catalogEntry) error {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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
		return scanCatalogFile(t, root, path, out)
	})
	if err != nil {
		return fmt.Errorf("walk catalogs: %w", err)
	}
	return nil
}

// scanCatalogFile records one test file's catalog rows.
func scanCatalogFile(t *testing.T, root, path string, out map[string]catalogEntry) error {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // repo-local test walk
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	body := string(raw)
	phases := make(map[int]bool)
	for _, m := range itemCall.FindAllStringSubmatch(body, -1) {
		perr := error(nil)
		var p int
		if p, perr = strconv.Atoi(m[1][1:]); perr != nil {
			t.Fatalf("PHASE GAGAL %s: phase %q: %v", path, m[1], perr)
		}
		phases[p] = true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("relative %s: %w", path, err)
	}
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
}

func readDoc(t *testing.T, root, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // repo-local docs
	if err != nil {
		t.Fatalf("PHASE GAGAL: %v", err)
	}
	return string(raw)
}
