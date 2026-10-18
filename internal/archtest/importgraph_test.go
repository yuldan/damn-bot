package archtest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// listedPackage = subset output `go list -json`.
type listedPackage struct {
	ImportPath   string
	Imports      []string
	TestImports  []string
	XTestImports []string
}

// importGOOS: file berkonstrain OS ikut diperiksa (impor di file _windows/_darwin tidak
// terlihat oleh go list GOOS=linux).
func importGOOS() []string { return []string{"linux", "windows", "darwin"} }

// listPackages menjalankan `go list -deps -json ./...` (tag goolm) untuk satu GOOS dan
// mengembalikan paket modul utama.
func listPackages(t *testing.T, goos string) []listedPackage {
	t.Helper()
	out := runRepo(t, exec.CommandContext(t.Context(), "go", "list", "-deps", "-tags=goolm",
		"-json=ImportPath,Imports,TestImports,XTestImports", "./..."), "GOOS="+goos)
	var pkgs []listedPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p listedPackage
		err := dec.Decode(&p)
		if errors.Is(err, io.EOF) {
			return pkgs
		}
		if err != nil {
			t.Fatalf("decode go list: %v", err)
		}
		if rel, ok := relPackage(p.ImportPath); ok {
			p.ImportPath = rel
			pkgs = append(pkgs, p)
		}
	}
}

// relPackage mengubah import path modul utama menjadi path relatif root modul.
func relPackage(importPath string) (string, bool) {
	return strings.CutPrefix(importPath, modulePath+"/")
}

// matrixRule mencari aturan paket: kunci persis, lalu wildcard "<induk>/*" untuk subpaket langsung.
func matrixRule(matrix map[string][]string, pkg string) ([]string, bool) {
	if allowed, ok := matrix[pkg]; ok {
		return allowed, true
	}
	if i := strings.LastIndex(pkg, "/"); i > 0 {
		allowed, ok := matrix[pkg[:i]+moduleWildcard]
		return allowed, ok
	}
	return nil, false
}

// checkEdge mengembalikan alasan pelanggaran edge from → to (internal), atau "" bila sah.
func checkEdge(matrix map[string][]string, from, to string, testEdge bool) string {
	testOnly := testOnlyPackages()
	fromAllowed, fromKnown := matrixRule(matrix, from)
	switch _, toKnown := matrixRule(matrix, to); {
	case !toKnown:
		return "paket internal " + to + " tidak ada di matriks allowed.go"
	case slices.Contains(testOnly, to) && !testEdge && !slices.Contains(testOnly, from):
		return "paket test-only diimpor kode produksi"
	case from == compositionRoot, testEdge && (to == from || slices.Contains(testOnly, to)):
		return ""
	case !fromKnown:
		return "paket internal " + from + " tidak ada di matriks allowed.go"
	case !slices.Contains(fromAllowed, to):
		return "edge tidak ada di matriks ENGINEERING §2.2.1"
	}
	return ""
}

// TestImportGraph: setiap edge impor internal → internal (produksi dan test, linux/windows/
// darwin) ada di matriks allowed.go; paket internal di luar matriks = gagal (E-01/E-02/E-06/E-08).
func TestImportGraph(t *testing.T) {
	t.Parallel()
	matrix := allowedImports()
	for pkg, allowed := range matrix {
		for _, to := range allowed {
			if _, ok := matrixRule(matrix, to); !ok {
				t.Errorf("allowed.go: %s mengizinkan %s yang bukan kunci matriks", pkg, to)
			}
		}
	}
	// Pelanggaran → GOOS tempat ia terlihat (edge yang sama dilaporkan sekali).
	violations := map[string][]string{}
	for _, goos := range importGOOS() {
		for _, p := range listPackages(t, goos) {
			for _, v := range packageViolations(matrix, &p) {
				violations[v] = append(violations[v], goos)
			}
		}
	}
	for _, v := range slices.Sorted(maps.Keys(violations)) {
		t.Errorf("E-01 edge ilegal: %s (GOOS=%s)", v, strings.Join(violations[v], ","))
	}
}

// packageViolations memeriksa paket p dan semua edge impor internalnya terhadap matriks.
func packageViolations(matrix map[string][]string, p *listedPackage) []string {
	var out []string
	if _, known := matrixRule(matrix, p.ImportPath); !known && p.ImportPath != compositionRoot {
		out = append(out, "paket internal "+p.ImportPath+" tidak ada di matriks allowed.go")
	}
	edges := func(imports []string, testEdge bool) {
		for _, imp := range imports {
			to, internal := relPackage(imp)
			if !internal {
				continue
			}
			if why := checkEdge(matrix, p.ImportPath, to, testEdge); why != "" {
				out = append(out, fmt.Sprintf("%s → %s (test=%v): %s", p.ImportPath, to, testEdge, why))
			}
		}
	}
	edges(p.Imports, false)
	edges(p.TestImports, true)
	edges(p.XTestImports, true)
	return out
}
