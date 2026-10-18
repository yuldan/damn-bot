// Package phase implements the incremental-gate semantics of the repo-root
// PHASE file (ADR-077 point 3, ADR-080, ADR-091 NEW2-22) for catalog-driven
// tests such as TestEdgeCaseCatalogSync and TestCLIDocsMatchHelp.
package phase

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// AcceptanceEnv switches to phase acceptance (`make phase-acceptance`):
// every item of a phase ≤ PHASE must be covered (ADR-080 point 2).
const AcceptanceEnv = "DAMNBOT_PHASE_ACCEPTANCE"

// Gate is the running phase read from PHASE.
type Gate struct {
	cur        int  // 1..9
	acceptance bool // AcceptanceEnv=1
}

// Current reads PHASE from the nearest ancestor directory of the working
// directory (tests run in their package directory).
func Current(tb testing.TB) Gate {
	tb.Helper()
	dir, err := os.Getwd()
	if err != nil {
		tb.Fatalf("PHASE GAGAL: %v", err)
	}
	for {
		b, err := readPhase(dir)
		switch {
		case err == nil:
			g, perr := Parse(string(b))
			if perr != nil {
				tb.Fatalf("PHASE GAGAL: %v", perr)
			}
			g.acceptance = os.Getenv(AcceptanceEnv) == "1"
			return g
		case !errors.Is(err, os.ErrNotExist):
			tb.Fatalf("PHASE GAGAL: %v", err)
		case filepath.Dir(dir) == dir:
			tb.Fatalf("PHASE GAGAL: file PHASE tidak ditemukan di atas direktori kerja")
		}
		dir = filepath.Dir(dir)
	}
}

// readPhase reads dir/PHASE through os.Root (no path traversal).
func readPhase(dir string) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", dir, err)
	}
	b, err := root.ReadFile("PHASE")
	if cerr := root.Close(); err == nil && cerr != nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("read PHASE: %w", err)
	}
	return b, nil
}

// Parse validates PHASE content: exactly one line "F1".."F9".
func Parse(s string) (Gate, error) {
	n, err := num(strings.TrimSuffix(s, "\n"))
	if err != nil {
		return Gate{}, fmt.Errorf("isi PHASE %q: %w", s, err)
	}
	return Gate{cur: n}, nil
}

func num(p string) (int, error) {
	if len(p) != 2 || p[0] != 'F' || p[1] < '1' || p[1] > '9' {
		return 0, fmt.Errorf("fase %q bukan F1..F9", p)
	}
	return int(p[1] - '0'), nil
}

// Item applies the gate to one catalog item whose owning phase is phase and
// whose status is "covered", "n/a" (reasoned; counts as covered) or
// "pending:<phase>". It reports whether the item's subtest must exist and
// run; violations are reported through tb.Errorf with a "PHASE GAGAL" prefix.
func (g Gate) Item(tb testing.TB, id, phase, status string) bool {
	tb.Helper()
	p, err := num(phase)
	if err != nil {
		tb.Errorf("PHASE GAGAL %s: %v", id, err)
		return false
	}
	switch {
	case status == "covered" || status == "n/a":
		return true // covered ahead of its phase is allowed (NEW2-22)
	case status != "pending:"+phase:
		tb.Errorf("PHASE GAGAL %s: status %q, want covered, n/a or pending:%s", id, status, phase)
	case g.cur == 9:
		tb.Errorf("PHASE GAGAL %s: pending at PHASE F9 (ADR-077)", id)
	case g.acceptance && p <= g.cur:
		tb.Errorf("PHASE GAGAL %s: phase acceptance F%d: item %s still pending", id, g.cur, phase)
	case p < g.cur:
		tb.Errorf("PHASE GAGAL %s: phase %s < PHASE F%d must be covered", id, phase, g.cur)
	}
	return false
}
