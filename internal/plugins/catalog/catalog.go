// Package catalog is the embedded, closed metadata catalog of the 645
// third-party plugin entries mapped by SPEC §9.10 / Q18 to MCP servers,
// skills or built-in modules.
//
// The catalog is data, not code: loading it never touches the network, the
// filesystem, or a shell, and an entry is never executed or installed. It
// answers "which plugin entries exist, how damnbot would consume each one,
// which upstream commit it is pinned to, and does the embedded row still hash
// to its recorded digest". Resolution against the live MCP/skill/module
// registries lives in internal/plugins/resolve; installation is not part of
// this package.
//
// The corpus follows the upstream hermes plugin-catalog/ snapshot: 645 YAML
// entries (removed.yaml is the kill list and is not counted) in nine
// categories plus two uncategorized ones. Kind is the damnbot consumption
// path, not an upstream field: an entry that declares plugin code
// (provides_tools/hooks/middleware/commands) is a module, an entry whose
// description declares a portable or hosted MCP server is mcp, a skill-only
// package is skill, and the rest fall back to module.
package catalog

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Exact corpus shape of the Q18 mapping (docs/F8-ACCEPTANCE.md PLUG-009).
const (
	TotalCount = 645
	maxDesc    = 400
)

// ErrUnknown is returned by [Catalog.Lookup] for an id outside the closed
// catalog.
var ErrUnknown = errors.New("plugins: unknown catalog entry")

// ErrInvalid is a malformed embedded row or an off-count corpus.
var ErrInvalid = errors.New("plugins: invalid catalog")

var (
	idRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	pinRe    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestRe = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

//go:embed plugins.tsv
var pluginsTSV embed.FS

// Kind is the damnbot consumption path of one entry.
type Kind string

// Catalog kinds. Every entry resolves through exactly one of them.
const (
	// KindMCP is a portable MCP package or hosted MCP server.
	KindMCP Kind = "mcp"
	// KindSkill is a package that ships skills only.
	KindSkill Kind = "skill"
	// KindModule is in-process plugin code that needs a damnbot module.
	KindModule Kind = "module"
)

// Valid reports whether k is a stored kind.
func (k Kind) Valid() bool { return k == KindMCP || k == KindSkill || k == KindModule }

// Entry is one embedded plugin row. Pin is the upstream full commit SHA from
// the catalog's `sha:` field; SHA256 is the digest of ID, Kind, Category, Pin
// and Description (see [digest]) and detects an edited embedded row.
type Entry struct {
	ID          string
	Kind        Kind
	Category    string
	Pin         string
	SHA256      string
	Description string
}

// Catalog is an immutable, id-indexed view of the embedded rows.
type Catalog struct {
	entries []Entry
	byID    map[string]int
}

// Load reads and validates the embedded catalog.
func Load() (*Catalog, error) {
	raw, err := pluginsTSV.ReadFile("plugins.tsv")
	if err != nil {
		return nil, fmt.Errorf("%w: read: %w", ErrInvalid, err)
	}
	return Parse(raw)
}

// Parse validates rows of the form
// `id<TAB>kind<TAB>category<TAB>pin<TAB>sha256<TAB>description`; a comment
// line starts with `#`. A duplicate or unsorted id, an unknown kind or
// category, a pin/digest that is not 40/64 lowercase hex, a description that
// is empty, too long or not one terminal-safe UTF-8 line, a digest mismatch,
// or a corpus that is not exactly [TotalCount] rows with the recorded category
// counts is invalid.
func Parse(raw []byte) (*Catalog, error) {
	c := &Catalog{byID: map[string]int{}}
	// A checkout with CRLF (Windows core.autocrlf) must hash and validate
	// exactly like the LF blob: drop every carriage return before splitting so
	// even a double-rewritten `\r\r\n` cannot leak into the description or
	// the digest.
	text := strings.ReplaceAll(string(raw), "\r", "")
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, line := range lines {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		e, err := parseRow(line)
		if err != nil {
			return nil, fmt.Errorf("%w: line %d: %w", ErrInvalid, i+1, err)
		}
		if prev, ok := c.byID[e.ID]; ok {
			return nil, fmt.Errorf("%w: line %d: duplicate id %q (first at row %d)", ErrInvalid, i+1, e.ID, prev+1)
		}
		if n := len(c.entries); n > 0 && c.entries[n-1].ID >= e.ID {
			return nil, fmt.Errorf("%w: line %d: ids must be strictly ascending (%q after %q)",
				ErrInvalid, i+1, e.ID, c.entries[n-1].ID)
		}
		c.byID[e.ID] = len(c.entries)
		c.entries = append(c.entries, e)
	}
	if err := c.validateCounts(); err != nil {
		return nil, err
	}
	return c, nil
}

func parseRow(line string) (Entry, error) {
	fields := strings.Split(line, "\t")
	if len(fields) != 6 {
		return Entry{}, fmt.Errorf("want 6 tab-separated fields, got %d", len(fields))
	}
	e := Entry{ID: fields[0], Kind: Kind(fields[1]), Category: fields[2], Pin: fields[3], SHA256: fields[4], Description: fields[5]}
	if !idRe.MatchString(e.ID) {
		return Entry{}, fmt.Errorf("id %q", e.ID)
	}
	if !e.Kind.Valid() {
		return Entry{}, fmt.Errorf("%s: kind %q", e.ID, e.Kind)
	}
	if _, ok := categoryCount(e.Category); !ok {
		return Entry{}, fmt.Errorf("%s: category %q", e.ID, e.Category)
	}
	if !pinRe.MatchString(e.Pin) {
		return Entry{}, fmt.Errorf("%s: pin %q is not a full lowercase commit SHA", e.ID, e.Pin)
	}
	if !digestRe.MatchString(e.SHA256) {
		return Entry{}, fmt.Errorf("%s: digest %q", e.ID, e.SHA256)
	}
	if err := validateDescription(e.Description); err != nil {
		return Entry{}, fmt.Errorf("%s: %w", e.ID, err)
	}
	if got := digest(&e); got != e.SHA256 {
		return Entry{}, fmt.Errorf("%s: digest mismatch (row %s, computed %s)", e.ID, e.SHA256, got)
	}
	return e, nil
}

func validateDescription(desc string) error {
	switch {
	case desc == "":
		return errors.New("empty description")
	case !utf8.ValidString(desc):
		return errors.New("description is not UTF-8")
	case strings.TrimSpace(desc) != desc:
		return errors.New("description has leading or trailing space")
	case utf8.RuneCountInString(desc) > maxDesc:
		return fmt.Errorf("description has %d runes (max %d)", utf8.RuneCountInString(desc), maxDesc)
	}
	for _, r := range desc {
		if r < 0x20 || r == 0x7f {
			return errors.New("description contains a control rune")
		}
	}
	return nil
}

// digest is the row digest: SHA-256 over the five stored fields, each
// terminated by exactly one '\n'. Description is included last so a trailing
// newline can not collide with another field.
func digest(e *Entry) string {
	h := sha256.New()
	for _, f := range []string{e.ID, string(e.Kind), e.Category, e.Pin, e.Description} {
		h.Write([]byte(f))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (c *Catalog) validateCounts() error {
	if len(c.entries) != TotalCount {
		return fmt.Errorf("%w: %d entries, want %d", ErrInvalid, len(c.entries), TotalCount)
	}
	byCat := map[string]int{}
	byKind := map[Kind]int{}
	for _, e := range c.entries {
		byCat[e.Category]++
		byKind[e.Kind]++
	}
	for cat := range byCat {
		want, _ := categoryCount(cat)
		if byCat[cat] != want {
			return fmt.Errorf("%w: category %s has %d entries, want %d", ErrInvalid, cat, byCat[cat], want)
		}
	}
	if want := len(expectedCategories()); len(byCat) != want {
		return fmt.Errorf("%w: %d categories, want %d", ErrInvalid, len(byCat), want)
	}
	for _, kv := range []struct {
		kind Kind
		want int
	}{{KindMCP, 59}, {KindSkill, 30}, {KindModule, 556}} {
		if byKind[kv.kind] != kv.want {
			return fmt.Errorf("%w: kind %s has %d entries, want %d", ErrInvalid, kv.kind, byKind[kv.kind], kv.want)
		}
	}
	return nil
}

// expectedCategories is the closed category table of the Q18 corpus.
func expectedCategories() []string {
	return []string{"-", "automation", "desktop", "general", "memory", "models", "platform", "tools", "voice", "web"}
}

// categoryCount returns the exact number of entries in one category.
func categoryCount(name string) (int, bool) {
	switch name {
	case "tools":
		return 198, true
	case "desktop":
		return 144, true
	case "memory":
		return 65, true
	case "models":
		return 56, true
	case "general":
		return 48, true
	case "automation":
		return 42, true
	case "platform":
		return 36, true
	case "web":
		return 36, true
	case "voice":
		return 18, true
	case "-":
		return 2, true
	}
	return 0, false
}

// Count returns the number of catalog entries.
func (c *Catalog) Count() int { return len(c.entries) }

// Entries returns a copy of the entries in ascending id order.
func (c *Catalog) Entries() []Entry { return slices.Clone(c.entries) }

// Lookup returns the entry with the exact id.
func (c *Catalog) Lookup(id string) (Entry, bool) {
	i, ok := c.byID[id]
	if !ok {
		return Entry{}, false
	}
	return c.entries[i], true
}
