package skills

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
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

// SideEffect is one declared `side_effects` value (closed set, SEC-15).
type SideEffect string

// Side effects a skill may declare. Payment makes every command that runs
// or names a file of the skill Monetary (SPEC §14.1).
const (
	EffectWrite   SideEffect = "write"
	EffectNetwork SideEffect = "network"
	EffectPayment SideEffect = "payment"
)

// Skill is one parsed SKILL.md plus the quarantine link. SideEffects is nil
// when the front matter has no `side_effects` key and empty for an explicit
// `side_effects: []`.
type Skill struct {
	ID          int64
	Name        string
	Description string
	Body        string
	Path        string
	SHA256      string
	Source      Source
	MemoryID    int64 // 0 = indexed directly (trusted source)
	SideEffects []SideEffect
	Requires    []string         // skill names this skill depends on
	Host        *HostRequirement // optional host gate (requires: {…}, GAP-TOOL-10)
	filePayment bool             // a lint finding found payment use in a file (SEC10-9)
}

// AddFindings records that lint found payment use in the skill's files, so
// the shell gate covers code that only appears in a script (SEC10-9).
func (s *Skill) AddFindings(findings []Finding) {
	for _, f := range findings {
		if f.Category == "payment" || f.Category == "payment_undeclared" {
			s.filePayment = true
		}
	}
}

// Payment reports whether the skill declares side_effects: [payment].
func (s *Skill) Payment() bool { return slices.Contains(s.SideEffects, EffectPayment) }

// Sensitive reports whether the skill must be treated as a payment skill by
// the shell gate: it declares payment, or its text carries a payment
// pattern without declaring one (SEC10-3). Files are covered by the lint
// finding of the same name.
func (s *Skill) Sensitive() bool {
	return s.Payment() || s.filePayment || containsAny(foldText(s.Description+"\n"+s.Body), paymentPatterns()) != ""
}

// PaymentDirs returns the directories of the sensitive skills in list: the
// shell classifier labels any command touching them Monetary (SEC-15). The
// caller passes every installed skill, including pending/quarantined ones
// (their files are already on disk), so a skill cannot avoid the gate by
// not being approved yet.
func PaymentDirs(list []Skill) []string {
	var out []string
	seen := map[string]bool{}
	for i := range list {
		if !list[i].Sensitive() {
			continue
		}
		d := filepath.Dir(list[i].Path)
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// Parse reads a SKILL.md (≤ [MaxBytes]) with a `---` front matter block
// holding `name:`, `description:` and the optional inline lists
// `side_effects: [payment]` and `requires: [other-skill]`. Unknown keys are
// ignored; folded or nested YAML is not supported and yields
// [ErrFrontMatter]. SHA256 is always computed from data, never trusted to
// the caller.
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
	if err := sk.parseLists(meta); err != nil {
		return Skill{}, err
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
	return s.validateLists()
}

// parseLists reads the optional side_effects and requires lists.
func (s *Skill) parseLists(meta map[string]string) error {
	if v, ok := meta["side_effects"]; ok {
		list, err := flowList(v)
		if err != nil {
			return err
		}
		s.SideEffects = make([]SideEffect, len(list))
		for i, e := range list {
			s.SideEffects[i] = SideEffect(e)
		}
	}
	if v, ok := meta["requires"]; ok {
		return s.parseRequires(v)
	}
	return nil
}

// parseRequires reads either the host object (GAP-TOOL-10) or the legacy list
// of skill names.
func (s *Skill) parseRequires(v string) error {
	if strings.HasPrefix(strings.TrimSpace(v), "{") {
		host, err := parseHostRequirement(v)
		if err != nil {
			return err
		}
		s.Host = &host
		return nil
	}
	list, err := flowList(v)
	if err != nil {
		return err
	}
	s.Requires = list
	return nil
}

// validateLists enforces the closed side_effects set and skill names.
func (s *Skill) validateLists() error {
	for _, e := range s.SideEffects {
		if e != EffectWrite && e != EffectNetwork && e != EffectPayment {
			return fmt.Errorf("%w: side_effects %q (write|network|payment)", ErrInvalid, e)
		}
	}
	for _, r := range s.Requires {
		if !validName(r) {
			return fmt.Errorf("%w: requires %q", ErrInvalid, r)
		}
	}
	return nil
}

// flowList parses `[a, b]` (or bare `a, b`). An empty value starts a block
// list, which the line parser cannot read: it fails closed rather than
// silently dropping a `payment` declaration.
func flowList(v string) ([]string, error) {
	if v == "" || strings.HasPrefix(v, "[") != strings.HasSuffix(v, "]") {
		return nil, fmt.Errorf("%w: list must be inline, e.g. [payment]", ErrFrontMatter)
	}
	out := []string{}
	for f := range strings.SplitSeq(strings.TrimSuffix(strings.TrimPrefix(v, "["), "]"), ",") {
		if f = unquote(strings.TrimSpace(f)); f != "" {
			out = append(out, f)
		}
	}
	return out, nil
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
