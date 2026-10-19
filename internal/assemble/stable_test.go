package assemble

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
)

func TestStableTierGolden(t *testing.T) {
	t.Parallel()
	variants := []struct {
		name string
		opts StableOptions
	}{
		{"f1", StableOptions{}},
		{"deferred", StableOptions{Deferred: true}},
		{"codeintel", StableOptions{Codeintel: true}},
	}
	for _, v := range variants {
		text := Stable(v.opts)
		checkGolden(t, filepath.Join("testdata", "stable-"+v.name+".txt"), []byte(text+"\n"))
		sum := sha256.Sum256([]byte(text))
		t.Logf("%s sha256=%s", v.name, hex.EncodeToString(sum[:]))
	}
}

// TestStableTierBudget enforces the F1 allocation of ADR-086 NEW-41 and the
// SPEC §6 coding-rule budget (≤ 120 tokens for the Coding section).
func TestStableTierBudget(t *testing.T) {
	t.Parallel()
	c := asciiCounter()
	f1 := Stable(StableOptions{})
	if got := c.Count(f1); got > 1000 {
		t.Errorf("F1 stable tier = %d tokens, allocation 1000", got)
	}
	_, coding, ok := strings.Cut(f1, "\nCoding:\n")
	if !ok {
		t.Fatal("Coding section missing")
	}
	if got := c.Count(coding); got > 120 {
		t.Errorf("Coding section = %d tokens, cap 120", got)
	}
	if got, want := len(strings.Split(strings.TrimSpace(coding), "\n")), 5; got != want {
		t.Errorf("F1 coding rules = %d, want %d", got, want)
	}
}

// TestStableRulesNumbered: conditional rules are removed and the remainder is
// numbered contiguously (NEW-52).
func TestStableRulesNumbered(t *testing.T) {
	t.Parallel()
	for _, opts := range []StableOptions{{}, {Deferred: true}, {Codeintel: true}, {Deferred: true, Codeintel: true}} {
		text := Stable(opts)
		want := 14
		if opts.Deferred {
			want++
		}
		for n := 1; n <= want; n++ {
			prefix := itoa(n) + ". "
			if !strings.Contains(text, "\n"+prefix) && !strings.HasPrefix(text, prefix) {
				t.Errorf("%+v: rule %d missing", opts, n)
			}
		}
		if strings.Contains(text, "\n"+itoa(want+1)+". ") {
			t.Errorf("%+v: rule %d exists, want %d rules", opts, want+1, want)
		}
	}
}

// TestStableVariantConditional locks the ADR-067 conditional rules.
func TestStableVariantConditional(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		opts     StableOptions
		contains []string
		absent   []string
	}{
		{"f1", StableOptions{}, []string{"grep and glob"}, []string{"tool_search", "repo_map"}},
		{"deferred", StableOptions{Deferred: true}, []string{"tool_search", "tool_invoke"}, []string{"repo_map"}},
		{"codeintel", StableOptions{Codeintel: true}, []string{"repo_map"}, []string{"tool_search"}},
		{"both", StableOptions{Deferred: true, Codeintel: true}, []string{"tool_search", "repo_map"}, nil},
	}
	for _, tc := range cases {
		text := Stable(tc.opts)
		for _, want := range tc.contains {
			if !strings.Contains(text, want) {
				t.Errorf("%s: missing %q", tc.name, want)
			}
		}
		for _, absent := range tc.absent {
			if strings.Contains(text, absent) {
				t.Errorf("%s: unexpected %q", tc.name, absent)
			}
		}
	}
}
