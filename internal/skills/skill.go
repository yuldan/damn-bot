package skills

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Limits of SPEC §4.8. MaxPromptDesc is the per-entry prompt truncation
// (SPEC §6: description is whole in the index, clipped only in the prompt).
const (
	MaxBytes      = 8192
	MaxName       = 64
	MaxPromptDesc = 120
	MaxOffer      = 3
)

// Source is where a skill came from (skills.source).
type Source string

// Skill sources.
const (
	SourceUser    Source = "user"
	SourceBuiltin Source = "builtin"
	SourceHub     Source = "hub"
	SourceAgent   Source = "agent"
	SourceImport  Source = "import"
)

// Valid reports whether s is a stored source.
func (s Source) Valid() bool {
	switch s {
	case SourceUser, SourceBuiltin, SourceHub, SourceAgent, SourceImport:
		return true
	}
	return false
}

// Trusted reports whether the source may be indexed without review. Only
// explicit user files and embedded builtins are trusted (P10, SPEC §4.8).
func (s Source) Trusted() bool { return s == SourceUser || s == SourceBuiltin }

// Status mirrors the linked memory decision (SPEC §4.8 diagram).
type Status string

// Skill lifecycle states (memory_decisions.status).
const (
	StatusPending  Status = "pending"
	StatusScanned  Status = "scanned"
	StatusApproved Status = "approved"
	StatusRejected Status = "rejected"
	StatusRetired  Status = "retired"
	StatusExpired  Status = "expired"
)

// Skill is one parsed SKILL.md plus the quarantine link.
type Skill struct {
	ID          int64
	Name        string
	Description string
	Body        string
	Path        string
	SHA256      string
	Source      Source
	MemoryID    int64 // 0 = indexed directly (trusted source)
}

// Parse reads a SKILL.md (≤ [MaxBytes]) with a `---` front matter block
// holding `name:` and `description:`. Unknown keys are ignored; folded or
// nested YAML is not supported and yields [ErrFrontMatter]. SHA256 is always
// computed from data, never trusted to the caller.
func Parse(data []byte, path string, source Source) (Skill, error) {
	if len(data) > MaxBytes {
		return Skill{}, fmt.Errorf("%w: skill > %d bytes", ErrTooLarge, MaxBytes)
	}
	sum := sha256.Sum256(data)
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF}) // JN3-10: accept UTF-8 BOM
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return Skill{}, fmt.Errorf("%w: missing opening delimiter", ErrFrontMatter)
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return Skill{}, fmt.Errorf("%w: missing closing delimiter", ErrFrontMatter)
	}
	meta := map[string]string{}
	for _, ln := range lines[1:end] {
		key, val, ok := strings.Cut(ln, ":")
		if !ok {
			continue
		}
		meta[strings.ToLower(strings.TrimSpace(key))] = unquote(strings.TrimSpace(val))
	}
	sk := Skill{
		Name:        meta["name"],
		Description: meta["description"],
		Body:        strings.TrimLeft(strings.Join(lines[end+1:], "\n"), "\n"),
		Path:        path,
		SHA256:      hex.EncodeToString(sum[:]),
		Source:      source,
	}
	if err := sk.Validate(); err != nil {
		return Skill{}, err
	}
	return sk, nil
}

// Validate checks the closed shape used by storage and retrieval.
func (s *Skill) Validate() error {
	if !s.Source.Valid() {
		return fmt.Errorf("%w: source %q", ErrInvalid, s.Source)
	}
	if !validName(s.Name) {
		return fmt.Errorf("%w: name %q", ErrInvalid, s.Name)
	}
	if strings.TrimSpace(s.Description) == "" {
		return fmt.Errorf("%w: empty description", ErrInvalid)
	}
	if hasUnsafe(s.Description, false) || hasUnsafe(s.Path, false) || hasUnsafe(s.Name, false) {
		return fmt.Errorf("%w: control/bidi character", ErrInvalid)
	}
	if hasUnsafe(s.Body, true) {
		return fmt.Errorf("%w: bidi character in body", ErrInvalid)
	}
	if strings.TrimSpace(s.Body) == "" {
		return fmt.Errorf("%w: empty body", ErrInvalid)
	}
	if strings.TrimSpace(s.Path) == "" {
		return fmt.Errorf("%w: empty path", ErrInvalid)
	}
	if len(s.SHA256) != 64 {
		return fmt.Errorf("%w: sha256", ErrInvalid)
	}
	return nil
}

// validName accepts the lowercase, hyphen/underscore form of bundled skills
// (`sdlc-review`, `google_workspace`), 1..[MaxName] bytes.
func validName(name string) bool {
	if name == "" || len(name) > MaxName {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case (r == '-' || r == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

// PromptLine is "- <name>: <description ≤120> (<path>)" with newlines
// flattened so a description cannot inject prompt structure.
func PromptLine(s *Skill) string {
	desc := oneLine(s.Description)
	if utf8.RuneCountInString(desc) > MaxPromptDesc {
		desc = string([]rune(desc)[:MaxPromptDesc-1]) + "…"
	}
	path := strings.NewReplacer("(", "%28", ")", "%29").Replace(oneLine(s.Path))
	return fmt.Sprintf("- %s: %s (%s)", oneLine(s.Name), desc, path)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(sanitizeUnsafe(s, true)), " ")
}

// sanitizeUnsafe drops control and bidi characters; layout characters are
// kept for bodies and flattened later by oneLine (JN3-11).
func sanitizeUnsafe(s string, allowLayout bool) string {
	var b strings.Builder
	for _, r := range s {
		if allowLayout && (r == '\n' || r == '\r' || r == '\t') {
			b.WriteRune(r)
			continue
		}
		if r < 0x20 || r == 0x7f || isBidi(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// hasUnsafe reports control or bidi characters; allowLayout keeps \n/\r/\t.
func hasUnsafe(s string, allowLayout bool) bool {
	for _, r := range s {
		if allowLayout && (r == '\n' || r == '\r' || r == '\t') {
			continue
		}
		if r < 0x20 || r == 0x7f || isBidi(r) {
			return true
		}
	}
	return false
}

func isBidi(r rune) bool {
	switch {
	case r >= 0x200B && r <= 0x200F, r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
		return v[1 : len(v)-1]
	}
	return v
}
