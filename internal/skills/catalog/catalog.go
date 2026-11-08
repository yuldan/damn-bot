// Package catalog is the embedded, closed metadata catalog of the 211 skills
// of the F8 SKCAT rows: 58 bundled skills in 12 categories under `skills/` and
// 153 optional skills in 23 categories under `optional-skills/`.
//
// The catalog is data, not code: loading it never touches the network, the
// filesystem, or a shell, so a catalog entry can be inspected before anything
// is installed. Install paths consume an entry through the skills hub, which
// downloads the upstream SKILL.md and runs the quarantine path; this package
// only answers "which skills exist, in which category, at which pinned
// version, and does the embedded row still hash to its recorded digest".
package catalog

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

// Version is the catalog revision carried by every entry. Upstream does not
// version individual SKILL.md files, so one catalog revision pins the whole
// corpus; an entry with another version fails [Parse].
const Version = "1.0.0"

// Exact corpus shape of the F8 ledger (docs/F8-ACCEPTANCE.md §6, SKCAT-001..035).
const (
	BundledCount  = 58
	OptionalCount = 153
	TotalCount    = 211
	maxDesc       = 512
)

// ErrUnknown is returned by [Catalog.Lookup] and [Catalog.Category] for a name
// or category outside the closed catalog.
var ErrUnknown = errors.New("skills: unknown catalog entry")

// ErrInvalid is a malformed embedded row.
var ErrInvalid = errors.New("skills: invalid catalog")

var (
	nameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	digestRe  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

//go:embed skills.tsv
var skillsTSV embed.FS

// Kind is the two top-level corpus roots.
type Kind string

// Catalog kinds.
const (
	KindBundled  Kind = "bundled"
	KindOptional Kind = "optional"
)

// Valid reports whether k is a stored kind.
func (k Kind) Valid() bool { return k == KindBundled || k == KindOptional }

// Dir is the repository directory of the kind's corpus.
func (k Kind) Dir() string {
	if k == KindBundled {
		return "skills"
	}
	return "optional-skills"
}

// Category is one SKCAT row: a kind, a directory name, and its exact count.
type Category struct {
	Kind  Kind
	Name  string
	Count int
}

// Entry is one embedded skill row. SHA256 is the digest of Path, Name and
// Description (see [Digest]); it detects an edited embedded row. The hub
// verifies the downloaded SKILL.md against the hash from the index, which is a
// different, content-level check.
type Entry struct {
	Path        string
	Name        string
	Description string
	Category    string
	Kind        Kind
	Version     string
	SHA256      string
}

// Catalog is an immutable, name-indexed view of the embedded rows.
type Catalog struct {
	entries    []Entry
	byName     map[string]int
	categories []Category
}

// Load reads and validates the embedded catalog.
func Load() (*Catalog, error) {
	raw, err := skillsTSV.ReadFile("skills.tsv")
	if err != nil {
		return nil, fmt.Errorf("%w: read: %w", ErrInvalid, err)
	}
	return Parse(raw)
}

// Parse validates catalog rows of the form
// `path<TAB>name<TAB>description<TAB>version<TAB>sha256`; a comment line
// starts with `#`. A duplicate name, an unknown kind/category, a path outside
// the kind's root, a bad name/version/hash, or an off-count corpus is invalid.
func Parse(raw []byte) (*Catalog, error) {
	c := &Catalog{byName: map[string]int{}}
	for i, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 5 {
			return nil, fmt.Errorf("%w: line %d: %d fields (want 5)", ErrInvalid, i+1, len(fields))
		}
		e := Entry{Path: fields[0], Name: fields[1], Description: fields[2], Version: fields[3], SHA256: fields[4]}
		if err := validateEntry(&e); err != nil {
			return nil, fmt.Errorf("%w: line %d: %w", ErrInvalid, i+1, err)
		}
		if _, dup := c.byName[e.Name]; dup {
			return nil, fmt.Errorf("%w: duplicate name %q", ErrInvalid, e.Name)
		}
		c.byName[e.Name] = len(c.entries)
		c.entries = append(c.entries, e)
	}
	if err := c.validateCounts(); err != nil {
		return nil, err
	}
	c.categories = buildCategories(c.entries)
	return c, nil
}

// validateCounts enforces the 58/153/211 corpus and every SKCAT category count.
func (c *Catalog) validateCounts() error {
	byKind := map[Kind]int{}
	byCat := map[string]int{}
	for _, e := range c.entries {
		byKind[e.Kind]++
		byCat[string(e.Kind)+"/"+e.Category]++
	}
	if err := countOrErr("bundled", byKind[KindBundled], BundledCount); err != nil {
		return err
	}
	if err := countOrErr("optional", byKind[KindOptional], OptionalCount); err != nil {
		return err
	}
	if len(c.entries) != TotalCount {
		return fmt.Errorf("%w: %d entries (want %d)", ErrInvalid, len(c.entries), TotalCount)
	}
	cats := expectedCategories()
	for _, cat := range cats {
		got := byCat[string(cat.Kind)+"/"+cat.Name]
		if err := countOrErr(cat.Kind.Dir()+"/"+cat.Name, got, cat.Count); err != nil {
			return err
		}
	}
	if len(byCat) != len(cats) {
		return fmt.Errorf("%w: %d categories (want %d)", ErrInvalid, len(byCat), len(cats))
	}
	return nil
}

func countOrErr(what string, got, want int) error {
	if got != want {
		return fmt.Errorf("%w: %s has %d skills (want %d)", ErrInvalid, what, got, want)
	}
	return nil
}

// validateEntry checks one row against the closed shape.
func validateEntry(e *Entry) error {
	if e.Kind == "" {
		if err := setKind(e); err != nil {
			return err
		}
	}
	if !e.Kind.Valid() {
		return fmt.Errorf("%w: kind %q", ErrInvalid, e.Kind)
	}
	if !nameRe.MatchString(e.Name) {
		return fmt.Errorf("%w: name %q", ErrInvalid, e.Name)
	}
	if !versionRe.MatchString(e.Version) || e.Version != Version {
		return fmt.Errorf("%w: version %q (want %s)", ErrInvalid, e.Version, Version)
	}
	if !digestRe.MatchString(e.SHA256) || e.SHA256 != Digest(e.Path, e.Name, e.Description) {
		return fmt.Errorf("%w: sha256 for %q", ErrInvalid, e.Name)
	}
	if err := validatePath(e); err != nil {
		return err
	}
	if strings.TrimSpace(e.Description) == "" || len(e.Description) > maxDesc || !utf8.ValidString(e.Description) {
		return fmt.Errorf("%w: description for %q", ErrInvalid, e.Name)
	}
	if strings.ContainsAny(e.Description, "\x00\n\r") {
		return fmt.Errorf("%w: control character in description for %q", ErrInvalid, e.Name)
	}
	return validateCategory(e)
}

// setKind derives the kind and category from the path root.
func setKind(e *Entry) error {
	root, rest, ok := strings.Cut(e.Path, "/")
	if !ok {
		return fmt.Errorf("%w: path %q", ErrInvalid, e.Path)
	}
	switch root {
	case KindBundled.Dir():
		e.Kind = KindBundled
	case KindOptional.Dir():
		e.Kind = KindOptional
	default:
		return fmt.Errorf("%w: root %q", ErrInvalid, root)
	}
	e.Category, _, _ = strings.Cut(rest, "/")
	return nil
}

// validatePath requires <kind>/<category>[/subdir]/<name>/SKILL.md, a clean
// slash path, and no traversal or absolute form.
func validatePath(e *Entry) error {
	if e.Path == "" || strings.ContainsAny(e.Path, "\\\x00") || path.IsAbs(e.Path) || path.Clean(e.Path) != e.Path {
		return fmt.Errorf("%w: path %q", ErrInvalid, e.Path)
	}
	for segment := range strings.SplitSeq(e.Path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("%w: path segment in %q", ErrInvalid, e.Path)
		}
	}
	if !strings.HasPrefix(e.Path, e.Kind.Dir()+"/") || !strings.HasSuffix(e.Path, "/"+e.Name+"/SKILL.md") {
		return fmt.Errorf("%w: path %q does not match %s/%s", ErrInvalid, e.Path, e.Kind, e.Name)
	}
	return nil
}

// validateCategory rejects a kind/category pair outside the SKCAT table.
func validateCategory(e *Entry) error {
	if e.Category == "" || !slices.ContainsFunc(expectedCategories(), func(c Category) bool {
		return c.Kind == e.Kind && c.Name == e.Category
	}) {
		return fmt.Errorf("%w: category %q", ErrInvalid, e.Category)
	}
	return nil
}

// expectedCategories is the closed SKCAT table. A new category needs a new
// ledger row, a test, and an entry here.
func expectedCategories() []Category {
	return []Category{
		{KindBundled, "apple", 4},
		{KindBundled, "autonomous-ai-agents", 5},
		{KindBundled, "creative", 10},
		{KindBundled, "devops", 1},
		{KindBundled, "email", 2},
		{KindBundled, "media", 3},
		{KindBundled, "note-taking", 1},
		{KindBundled, "productivity", 14},
		{KindBundled, "research", 4},
		{KindBundled, "social-media", 1},
		{KindBundled, "software-development", 12},
		{KindBundled, "web", 1},
		{KindOptional, "autonomous-ai-agents", 7},
		{KindOptional, "blockchain", 3},
		{KindOptional, "communication", 1},
		{KindOptional, "creative", 31},
		{KindOptional, "data-science", 1},
		{KindOptional, "devops", 7},
		{KindOptional, "dogfood", 1},
		{KindOptional, "email", 1},
		{KindOptional, "finance", 9},
		{KindOptional, "gaming", 2},
		{KindOptional, "health", 2},
		{KindOptional, "mcp", 3},
		{KindOptional, "migration", 1},
		{KindOptional, "mlops", 36},
		{KindOptional, "payments", 3},
		{KindOptional, "productivity", 10},
		{KindOptional, "research", 15},
		{KindOptional, "security", 6},
		{KindOptional, "smart-home", 1},
		{KindOptional, "social-media", 1},
		{KindOptional, "software-development", 6},
		{KindOptional, "web-development", 5},
		{KindOptional, "yuanbao", 1},
	}
}

// buildCategories returns the categories in the fixed SKCAT order.
func buildCategories(entries []Entry) []Category {
	out := expectedCategories()
	for i := range out {
		out[i].Count = countCategory(entries, out[i])
	}
	return out
}

func countCategory(entries []Entry, c Category) int {
	n := 0
	for _, e := range entries {
		if e.Kind == c.Kind && e.Category == c.Name {
			n++
		}
	}
	return n
}

// Digest is the SHA-256 of the tab-free canonical row fields. It changes when
// the embedded path, name, or description changes, so [Parse] rejects an
// edited row whose recorded hash was not regenerated.
func Digest(p, name, description string) string {
	sum := sha256.Sum256([]byte(p + "\n" + name + "\n" + description))
	return hex.EncodeToString(sum[:])
}

// Entries returns a copy of every entry in corpus order.
func (c *Catalog) Entries() []Entry { return slices.Clone(c.entries) }

// Lookup returns the entry with the exact name.
func (c *Catalog) Lookup(name string) (Entry, error) {
	i, ok := c.byName[name]
	if !ok {
		return Entry{}, fmt.Errorf("%w: %q", ErrUnknown, name)
	}
	return c.entries[i], nil
}

// Categories returns a copy of the SKCAT table with its counts.
func (c *Catalog) Categories() []Category { return slices.Clone(c.categories) }

// Count returns the number of entries of one kind.
func (c *Catalog) Count(k Kind) int {
	n := 0
	for _, e := range c.entries {
		if e.Kind == k {
			n++
		}
	}
	return n
}

// Category returns the entries of one kind/category pair in corpus order.
func (c *Catalog) Category(k Kind, name string) ([]Entry, error) {
	if !slices.ContainsFunc(c.categories, func(cc Category) bool { return cc.Kind == k && cc.Name == name }) {
		return nil, fmt.Errorf("%w: category %s/%s", ErrUnknown, k, name)
	}
	out := make([]Entry, 0, 8)
	for _, e := range c.entries {
		if e.Kind == k && e.Category == name {
			out = append(out, e)
		}
	}
	return out, nil
}
