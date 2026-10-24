package skills

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestBundleRoundTrip: save/load preserves aliases and members sorted by map
// marshal; a missing file is empty.
func TestBundleRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "bundles.json")
	if got, err := LoadBundles(path); err != nil || len(got) != 0 {
		t.Fatalf("missing file = %v/%v, want empty", got, err)
	}
	in := map[string]Bundle{
		"lint":   {Name: "lint", Skills: []string{"go-lint", "shellcheck"}},
		"review": {Name: "review", Skills: []string{"diff-review"}},
	}
	if err := SaveBundles(path, in); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Windows cannot express 0600: a writable file reports 0666 there, so the
	// POSIX mode assertion only makes sense off Windows (repo pattern).
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", fi.Mode().Perm())
	}
	out, err := LoadBundles(path)
	if err != nil || len(out) != 2 || out["lint"].Skills[1] != "shellcheck" {
		t.Fatalf("load = %v, %v", out, err)
	}
}

// TestBundleValidation: names, counts, duplicates and unknown members are
// rejected; Resolve returns a copy.
func TestBundleValidation(t *testing.T) {
	t.Parallel()
	known := func(n string) bool { return n == "a" || n == "b" }
	bad := []Bundle{
		{Name: "Bad Name", Skills: []string{"a"}},
		{Name: "x", Skills: nil},
		{Name: "x", Skills: []string{"a", "a"}},
		{Name: "x", Skills: []string{"missing"}},
		{Name: "x", Skills: make([]string, MaxBundleSkills+1)},
	}
	for _, b := range bad {
		if err := ValidateBundle(b, known); !errors.Is(err, ErrBundle) {
			t.Errorf("%+v accepted: %v", b, err)
		}
	}
	ok := Bundle{Name: "pair", Skills: []string{"a", "b"}}
	got, err := ok.Resolve(known)
	if err != nil || len(got) != 2 {
		t.Fatalf("resolve = %v, %v", got, err)
	}
	got[0] = "mutated"
	if ok.Skills[0] != "a" {
		t.Error("Resolve returned the backing slice")
	}
}
