package hub

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/skills"
)

// maxIndexEntries bounds one index document (registry-wide, not one page).
const maxIndexEntries = 4096

// Index is one source's signed manifest in its decoded form. Schema is the
// index format revision; only schema 1 is accepted.
type Index struct {
	Source  Source
	Schema  int
	Entries []Entry
}

// Entry is one installable skill in a source index. SHA256 is mandatory and
// covers the downloaded SKILL.md bytes; ToS is mandatory metadata (GAP-GW-07).
type Entry struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	SHA256      string `json:"sha256"`
	Path        string `json:"path"`
	Description string `json:"description"`
	Size        int64  `json:"size"`
	// Commit is the pinned source revision. The GitHub source requires a
	// 40-hex commit SHA (SKILL-013); other sources may omit it.
	Commit string `json:"commit,omitempty"`
	ToS    skills.ToSMeta
}

var (
	entryNameRe    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	entryVersionRe = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	entryHashRe    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	entryCommitRe  = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// ParseIndex decodes and validates a source index. Each entry must carry a
// valid tos_risk/tos_note pair (through [skills.ParseToSMeta]), a pinned
// sha256, a relative SKILL.md path, and a size no larger than MaxSkillBytes.
func ParseIndex(src Source, raw []byte) (Index, error) {
	if len(raw) == 0 || len(raw) > MaxIndexBytes {
		return Index{}, fmt.Errorf("%w: %d bytes", ErrIndex, len(raw))
	}
	var doc struct {
		Schema  int               `json:"schema"`
		Entries []json.RawMessage `json:"entries"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return Index{}, fmt.Errorf("%w: %w", ErrIndex, err)
	}
	if doc.Schema != 1 {
		return Index{}, fmt.Errorf("%w: schema %d", ErrIndex, doc.Schema)
	}
	if len(doc.Entries) == 0 || len(doc.Entries) > maxIndexEntries {
		return Index{}, fmt.Errorf("%w: %d entries", ErrIndex, len(doc.Entries))
	}
	idx := Index{Source: src, Schema: doc.Schema, Entries: make([]Entry, 0, len(doc.Entries))}
	seen := make(map[string]bool, len(doc.Entries))
	for i, rawEntry := range doc.Entries {
		e, err := parseEntry(rawEntry)
		if err != nil {
			return Index{}, fmt.Errorf("%w: entry %d: %w", ErrIndex, i, err)
		}
		if src == SourceGitHub && !entryCommitRe.MatchString(e.Commit) {
			return Index{}, fmt.Errorf("%w: entry %d: commit %q", ErrIndex, i, e.Commit)
		}
		if seen[e.Name] {
			return Index{}, fmt.Errorf("%w: duplicate %q", ErrIndex, e.Name)
		}
		seen[e.Name] = true
		idx.Entries = append(idx.Entries, e)
	}
	return idx, nil
}

// parseEntry decodes one entry and its mandatory ToS metadata.
func parseEntry(raw json.RawMessage) (Entry, error) {
	var wire struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		SHA256      string `json:"sha256"`
		Path        string `json:"path"`
		Description string `json:"description"`
		Size        int64  `json:"size"`
		Commit      string `json:"commit"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return Entry{}, fmt.Errorf("%w: %w", ErrIndex, err)
	}
	tos, err := skills.ParseToSMeta(raw)
	if err != nil {
		return Entry{}, fmt.Errorf("%w: %w", ErrIndex, err)
	}
	e := Entry{
		Name: wire.Name, Version: wire.Version, SHA256: wire.SHA256,
		Path: wire.Path, Description: wire.Description, Size: wire.Size,
		Commit: wire.Commit, ToS: tos,
	}
	if err := e.validate(); err != nil {
		return Entry{}, err
	}
	return e, nil
}

// validate enforces the closed entry shape. Path is slash-relative and ends
// in /SKILL.md; no segment may be empty, ".", or "..".
func (e *Entry) validate() error {
	if !entryNameRe.MatchString(e.Name) {
		return fmt.Errorf("%w: name %q", ErrIndex, e.Name)
	}
	if !entryVersionRe.MatchString(e.Version) {
		return fmt.Errorf("%w: version %q", ErrIndex, e.Version)
	}
	if !entryHashRe.MatchString(e.SHA256) {
		return fmt.Errorf("%w: sha256 for %q", ErrIndex, e.Name)
	}
	if e.Size <= 0 || e.Size > MaxSkillBytes {
		return fmt.Errorf("%w: size %d for %q", ErrIndex, e.Size, e.Name)
	}
	if err := validateEntryPath(e.Path); err != nil {
		return err
	}
	if strings.TrimSpace(e.Description) == "" || len(e.Description) > 512 || !utf8.ValidString(e.Description) {
		return fmt.Errorf("%w: description for %q", ErrIndex, e.Name)
	}
	if strings.ContainsAny(e.Description, "\x00\n\r") {
		return fmt.Errorf("%w: control character in description for %q", ErrIndex, e.Name)
	}
	if err := e.ToS.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrIndex, err)
	}
	return nil
}

func validateEntryPath(p string) error {
	if p == "" || strings.ContainsAny(p, "\\\x00") || strings.HasPrefix(p, "/") || path.Clean(p) != p {
		return fmt.Errorf("%w: path %q", ErrIndex, p)
	}
	if !strings.HasSuffix(p, "/SKILL.md") {
		return fmt.Errorf("%w: path %q does not end in /SKILL.md", ErrIndex, p)
	}
	for segment := range strings.SplitSeq(p, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("%w: path segment in %q", ErrIndex, p)
		}
	}
	return nil
}

// Find returns one entry by exact name.
func (i Index) Find(name string) (Entry, error) {
	if n := slices.IndexFunc(i.Entries, func(e Entry) bool { return e.Name == name }); n >= 0 {
		return i.Entries[n], nil
	}
	return Entry{}, fmt.Errorf("%w: %q", ErrNoEntry, name)
}
