//go:build licenses

package archtest

import (
	"bytes"
	"encoding/csv"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// mplLicense = satu-satunya lisensi yang dibatasi deps.mpl (E-40).
const mplLicense = "MPL-2.0"

// allowedLicenses = E-40 (sama dengan LICENSES_ALLOWED Makefile).
func allowedLicenses() []string {
	return []string{"MIT", "BSD-2-Clause", "BSD-3-Clause", "Apache-2.0", "ISC", mplLicense}
}

// TestMPLIsolation (E-40, ADR-078 AUD-069): `go-licenses report` atas binary (cmd + internal,
// tag goolm) hanya memuat lisensi E-40, dan setiap library MPL-2.0 berada di modul deps.mpl.
// Hanya jalan dengan tag `licenses` (butuh go-licenses dari tools/go.mod).
func TestMPLIsolation(t *testing.T) {
	t.Parallel()
	mpl := allowlist(readRepoFile(t, "deps.mpl"))
	// go-licenses tidak punya flag -tags: tag lewat GOFLAGS, sama dengan target `make licenses`.
	out := runRepo(t, exec.CommandContext(t.Context(), "go", "tool", "-modfile=tools/go.mod",
		"go-licenses", "report", "./cmd/...", "./internal/..."), "GOFLAGS=-tags=goolm")
	records, err := csv.NewReader(bytes.NewReader(out)).ReadAll()
	if err != nil {
		t.Fatalf("parse go-licenses report: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("go-licenses report kosong")
	}
	for _, r := range records {
		if len(r) != 3 {
			t.Errorf("go-licenses report: baris tidak valid %q", r)
			continue
		}
		lib, license := r[0], r[2]
		switch {
		case !slices.Contains(allowedLicenses(), license):
			t.Errorf("E-40 lisensi ditolak: %s (%s)", lib, license)
		case license == mplLicense && !slices.ContainsFunc(mpl, func(mod string) bool {
			return lib == mod || strings.HasPrefix(lib, mod+"/")
		}):
			t.Errorf("E-40 MPL-2.0 di luar deps.mpl: %s (tambah lewat ADR)", lib)
		}
	}
}
