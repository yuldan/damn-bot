package archtest

import (
	"encoding/json"
	"go/ast"
	"os"
	"path"
	"slices"
	"strings"
	"testing"
)

// skipBaseline is the committed per-package skip ceiling.
type skipBaseline struct {
	Total    int            `json:"total"`
	Packages map[string]int `json:"packages"`
}

// TestSkipRatchet (GAP-REL-15, E-73/E-74): the number of skip markers per
// package may never rise above the committed baseline, and CI workflows may
// not retry failed tests into green.
func TestSkipRatchet(t *testing.T) {
	t.Parallel()
	base := readSkipBaseline(t)
	got, total := skipsByPackage(t)
	for dir, n := range got {
		if limit, ok := base.Packages[dir]; ok && n > limit {
			t.Errorf("skip ratchet: %s has %d skips, baseline %d", dir, n, limit)
		}
		if _, ok := base.Packages[dir]; !ok {
			t.Errorf("skip ratchet: %s adds %d skips without a baseline row", dir, n)
		}
	}
	if total > base.Total {
		t.Errorf("skip ratchet: total %d > baseline %d", total, base.Total)
	}
	for _, wf := range workflowFiles(t) {
		for line := range strings.Lines(wf.body) {
			low := strings.ToLower(line)
			if strings.Contains(low, "-retry") || strings.Contains(low, "--retry") ||
				strings.Contains(low, "rerun") || strings.Contains(low, "retry make test") {
				t.Errorf("%s retries tests: %q", wf.name, strings.TrimSpace(line))
			}
		}
	}
}

// readSkipBaseline parses testdata/skip-baseline.json.
func readSkipBaseline(t *testing.T) skipBaseline {
	t.Helper()
	raw, err := os.ReadFile(path.Join("testdata", "skip-baseline.json"))
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	var base skipBaseline
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatalf("parse baseline: %v", err)
	}
	return base
}

// skipsByPackage counts skip calls in main-module test files.
func skipsByPackage(t *testing.T) (map[string]int, int) {
	t.Helper()
	got := map[string]int{}
	total := 0
	for _, f := range mainModuleGoFiles(t) {
		if !f.isTest() {
			continue
		}
		n := countSkips(f)
		if n == 0 {
			continue
		}
		got[path.Dir(f.path)] += n
		total += n
	}
	return got, total
}

// countSkips counts calls to testing.T.Skip/Skipf/SkipNow (or B variants).
func countSkips(f goFile) int {
	n := 0
	ast.Inspect(f.ast, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Skip", "Skipf", "SkipNow":
			n++
		}
		return true
	})
	return n
}

// workflow is one CI workflow file.
type workflow struct{ name, body string }

// workflowFiles reads every workflow YAML in .github/workflows.
func workflowFiles(t *testing.T) []workflow {
	t.Helper()
	dir := repoDir() + "/.github/workflows"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read workflows: %v", err)
	}
	var out []workflow
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && (strings.HasSuffix(e.Name(), ".yml") || strings.HasSuffix(e.Name(), ".yaml")) {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names)
	for _, name := range names {
		body, err := os.ReadFile(path.Join(dir, name)) //nolint:gosec // G304: name comes from ReadDir of the repo
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		out = append(out, workflow{name: name, body: string(body)})
	}
	return out
}
