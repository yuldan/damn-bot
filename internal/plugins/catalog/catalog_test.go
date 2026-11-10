package catalog

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// mustLoad loads the embedded corpus or fails the test.
func mustLoad(t *testing.T) *Catalog {
	t.Helper()
	c, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

const testPin = "0123456789abcdef0123456789abcdef01234567"

// TestF8PluginCatalog645 pins the Q18 corpus shape: 645 entries, every
// category count from the parity row, all three consumption paths, unique
// sorted ids and a full upstream commit pin per entry.
func TestF8PluginCatalog645(t *testing.T) {
	t.Parallel()
	c := mustLoad(t)
	if got := c.Count(); got != 645 {
		t.Fatalf("Count = %d, want 645", got)
	}
	entries := c.Entries()
	byCat := map[string]int{}
	byKind := map[Kind]int{}
	for i, e := range entries {
		if i > 0 && entries[i-1].ID >= e.ID {
			t.Errorf("entries not sorted: %q after %q", e.ID, entries[i-1].ID)
		}
		byCat[e.Category]++
		byKind[e.Kind]++
		if !idRe.MatchString(e.ID) {
			t.Errorf("%s: invalid id", e.ID)
		}
		if !pinRe.MatchString(e.Pin) {
			t.Errorf("%s: pin %q is not a 40-hex commit", e.ID, e.Pin)
		}
		if e.Description == "" || utf8.RuneCountInString(e.Description) > maxDesc {
			t.Errorf("%s: description shape", e.ID)
		}
	}
	wantCat := map[string]int{
		"tools": 198, "desktop": 144, "memory": 65, "models": 56, "general": 48,
		"automation": 42, "platform": 36, "web": 36, "voice": 18, "-": 2,
	}
	for cat, want := range wantCat {
		if byCat[cat] != want {
			t.Errorf("category %s = %d, want %d", cat, byCat[cat], want)
		}
	}
	if len(byCat) != len(wantCat) {
		t.Errorf("categories = %d, want %d", len(byCat), len(wantCat))
	}
	wantKind := map[Kind]int{KindMCP: 59, KindSkill: 30, KindModule: 556}
	for kind, want := range wantKind {
		if byKind[kind] != want {
			t.Errorf("kind %s = %d, want %d", kind, byKind[kind], want)
		}
	}
	if len(byKind) != len(wantKind) {
		t.Errorf("kinds = %d, want %d", len(byKind), len(wantKind))
	}
}

// TestF8PluginCatalogClosed proves the registry is closed: an id outside the
// corpus is unknown, and a parsed corpus that is off-count, duplicated,
// unsorted, or carries an unknown kind/category/pin/description is rejected.
func TestF8PluginCatalogClosed(t *testing.T) {
	t.Parallel()
	c := mustLoad(t)
	if _, ok := c.Lookup("not-a-real-plugin-xyz"); ok {
		t.Fatal("Lookup accepted an id outside the closed catalog")
	}
	if e, ok := c.Lookup("0bull"); !ok || e.Kind != KindMCP {
		t.Fatalf("Lookup(0bull) = (%+v, %t), want the mcp entry", e, ok)
	}

	rows := dataRows(mustReadTSV(t))
	if len(rows) != 645 {
		t.Fatalf("fixture rows = %d", len(rows))
	}
	valid := strings.Join(rows, "\n") + "\n"
	if _, err := Parse([]byte(valid)); err != nil {
		t.Fatalf("Parse(valid fixture): %v", err)
	}
	duplicated := append([]string{rows[0]}, rows...)
	badDigest := append([]string{}, rows...)
	badDigest[0] = setField(badDigest[0], 4, strings.Repeat("0", 64))
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"off count", strings.Join(rows[1:], "\n") + "\n", "645"},
		{"duplicate id", strings.Join(duplicated, "\n") + "\n", "duplicate id"},
		{"extra field", valid + "\textra\n", "6 tab-separated fields"},
		{"unknown kind", strings.Replace(valid, "\tmcp\t", "\tplugin\t", 1), "kind"},
		{"unknown category", strings.Replace(valid, "\ttools\t", "\tshelf\t", 1), "category"},
		{"bad pin", strings.Replace(valid, "240890a71715375bc877b238491e414517e9d902", "240890a7", 1), "pin"},
		{"empty description", strings.Replace(valid, "\tControl real iPhones", "\t", 1), "description"},
		{"digest mismatch", strings.Join(badDigest, "\n") + "\n", "digest mismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse([]byte(tc.raw))
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Parse = %v, want ErrInvalid", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Parse error %q does not mention %q", err, tc.want)
			}
		})
	}
	// Entries is a copy: mutating it never changes the catalog.
	entries := c.Entries()
	entries[0].Kind = KindModule
	if again, _ := c.Lookup(entries[0].ID); again.Kind != KindMCP {
		t.Fatal("Entries returned a view into the immutable catalog")
	}
}

// setField replaces one tab-separated field of a fixture row.
func setField(row string, idx int, val string) string {
	fields := strings.Split(row, "\t")
	fields[idx] = val
	return strings.Join(fields, "\t")
}

// dataRows returns the non-comment, non-empty fixture rows.
func dataRows(raw []byte) []string {
	var out []string
	for line := range strings.SplitSeq(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

// mustReadTSV returns the embedded catalog bytes.
func mustReadTSV(t *testing.T) []byte {
	t.Helper()
	raw, err := pluginsTSV.ReadFile("plugins.tsv")
	if err != nil {
		t.Fatalf("read embedded catalog: %v", err)
	}
	return raw
}

// TestF8PluginCatalogDigest recomputes every recorded digest and proves that
// editing any field of a row invalidates it.
func TestF8PluginCatalogDigest(t *testing.T) {
	t.Parallel()
	for _, e := range mustLoad(t).Entries() {
		if got := digest(&e); got != e.SHA256 {
			t.Errorf("%s: digest %s, computed %s", e.ID, e.SHA256, got)
		}
	}
	base := Entry{ID: "digest-probe", Kind: KindMCP, Category: "tools", Pin: testPin, Description: "A probe entry."}
	base.SHA256 = digest(&base)
	check := []struct {
		name string
		mut  func(*Entry)
	}{
		{"id", func(e *Entry) { e.ID = "digest-probe-2" }},
		{"kind", func(e *Entry) { e.Kind = KindSkill }},
		{"category", func(e *Entry) { e.Category = "memory" }},
		{"pin", func(e *Entry) { e.Pin = strings.Repeat("f", 40) }},
		{"description", func(e *Entry) { e.Description = "A probe entry!" }},
	}
	for _, tc := range check {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mutated := base
			tc.mut(&mutated)
			if got := digest(&mutated); got == base.SHA256 {
				t.Fatalf("%s change did not invalidate the digest", tc.name)
			}
		})
	}
	tampered := strings.Replace(string(mustReadTSV(t)), "Control real iPhones", "Control fake iPhones", 1)
	if _, err := Parse([]byte(tampered)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Parse(tampered row) = %v, want ErrInvalid", err)
	}
}

// TestF8PLUG009PluginCatalog is the named ledger evidence for PLUG-009: the
// catalog is SHA-pinned, closed and tamper-evident, and loading it executes
// nothing. Signature and anti-rollback for signed provider/MCP manifests are
// proved by internal/mcp/catalog (GAP-SEC-20) and are not duplicated here.
func TestF8PLUG009PluginCatalog(t *testing.T) {
	t.Parallel()
	c := mustLoad(t)
	if c.Count() != TotalCount {
		t.Fatalf("Count = %d, want %d", c.Count(), TotalCount)
	}
	for _, e := range c.Entries() {
		if len(e.Pin) != 40 {
			t.Fatalf("%s: pin length %d, want a full commit SHA", e.ID, len(e.Pin))
		}
		if e.Pin == strings.Repeat("0", 40) {
			t.Errorf("%s: all-zero pin is not an upstream commit", e.ID)
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(string) string
	}{
		{"edited description", func(s string) string { return strings.Replace(s, "Control real iPhones", "Control real Phones", 1) }},
		{"edited pin", func(s string) string {
			return strings.Replace(s, "240890a71715375bc877b238491e414517e9d902", strings.Repeat("1", 40), 1)
		}},
		{"edited digest", func(s string) string {
			lines := dataRows([]byte(s))
			lines[0] = setField(lines[0], 4, strings.Repeat("2", 64))
			return strings.Join(lines, "\n") + "\n"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Parse([]byte(tc.mutate(string(mustReadTSV(t))))); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Parse = %v, want ErrInvalid for a broken entry", err)
			}
		})
	}
}
