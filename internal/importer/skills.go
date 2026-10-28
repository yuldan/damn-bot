package importer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/skills"
)

// maxSkillBytes is the SKILL.md cap of SPEC §4.8.
const maxSkillBytes = 8192

func (i *Importer) collectSkills(root string, c *collected) error {
	dir := filepath.Join(root, "skills")
	err := i.deps.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil && errors.Is(walkErr, fs.ErrNotExist) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&fs.ModeSymlink != 0 && d.Name() != "SKILL.md" {
			c.report.skip(SourceSkills, filepath.ToSlash(path), "symlink is not followed")
			return nil
		}
		if d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}
		meta, body, data, err := i.readSkillFile(path)
		if err != nil {
			return err
		}
		origin, relErr := filepath.Rel(root, filepath.Dir(path))
		if relErr != nil {
			return fmt.Errorf("importer: relative skill path: %w", relErr)
		}
		c.addSkill(meta, body, filepath.ToSlash(origin), data)
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

// readSkillFile reads one SKILL.md, rejecting an oversized or non-UTF-8 file
// before parsing.
func (i *Importer) readSkillFile(path string) (map[string]any, string, []byte, error) {
	data, err := i.readOptional(path)
	if err != nil {
		return nil, "", nil, err
	}
	if len(data) > maxSkillBytes || !utf8.Valid(data) {
		return nil, "", nil, syntaxError(path, fmt.Errorf("file is not UTF-8 or exceeds %d bytes", maxSkillBytes))
	}
	meta, body, err := splitFrontmatter(data, path)
	if err != nil {
		return nil, "", nil, err
	}
	return meta, body, data, nil
}

// addSkill records one parsed SKILL.md; an invalid item is a reported skip.
func (c *collected) addSkill(meta map[string]any, body, origin string, data []byte) {
	name := childString(meta, "name")
	if !validSkillName(name) {
		c.report.skip(SourceSkills, origin, "SKILL.md has no valid frontmatter name")
		return
	}
	description := childString(meta, "description")
	if description == "" || unsafeText(name) || unsafeText(description) {
		c.report.skip(SourceSkills, name, "SKILL.md description is missing or unsafe")
		return
	}
	if c.reject(SourceSkills, name) {
		return
	}
	sum := sha256.Sum256(data)
	item := Skill{
		Name: name, Description: description, Body: body,
		Origin: origin, SHA256: hex.EncodeToString(sum[:]), Runtime: runtimeRefs(body),
		Data: append([]byte(nil), data...),
	}
	item.setToS()
	c.skills = append(c.skills, item)
	c.report.tos(SourceSkills, name, skills.ToSMeta{Risk: item.ToSRisk, Note: item.ToSNote})
	c.report.item(SourceSkills)
}

// setToS derives the GAP-GW-07 metadata from the entry text.
func (s *Skill) setToS() {
	meta := skills.ClassifyToS(s.Name + "\n" + s.Description + "\n" + s.Body)
	s.ToSRisk, s.ToSNote = meta.Risk, meta.Note
}

// validSkillName mirrors the closed name shape the skill store accepts.
func validSkillName(name string) bool {
	if name == "" || len(name) > 64 {
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

// unsafeText reports control and format runes (bidi, zero-width) the skill
// store rejects.
func unsafeText(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || unicode.In(r, unicode.Cf)
	})
}

// collectSkillTree imports <dir>/<name>/SKILL.md children and reports skips.
func collectSkillTree(read func(string) ([]byte, error), walkDir func(string, fs.WalkDirFunc) error,
	root, dir, prefix string, report *Report, source Source, out *[]Skill,
) (bool, error) {
	found := false
	err := walkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if d.Type()&fs.ModeSymlink != 0 && d.Name() != "SKILL.md" {
			report.skip(source, prefix+filepath.ToSlash(path), "symlink is not followed")
			return nil
		}
		if d.IsDir() || d.Name() != "SKILL.md" || filepath.Dir(filepath.Dir(path)) != dir {
			return nil
		}
		found = true
		skill, reason, err := readSkillAt(read, root, path)
		if err != nil {
			return err
		}
		key := skill.Name
		if key == "" {
			key = skill.Origin
		}
		if reason != "" {
			report.skip(source, prefix+key, reason)
			return nil
		}
		report.tos(source, prefix+key, skills.ToSMeta{Risk: skill.ToSRisk, Note: skill.ToSNote})
		*out = append(*out, skill)
		report.item(source)
		return nil
	})
	return found, err
}

func readSkillAt(read func(string) ([]byte, error), root, path string) (Skill, string, error) {
	data, err := readOptionalFile(read, path)
	if err != nil {
		return Skill{}, "", err
	}
	if len(data) > maxSkillBytes || !utf8.Valid(data) {
		return Skill{}, "", syntaxError(path, fmt.Errorf("file is not UTF-8 or exceeds %d bytes", maxSkillBytes))
	}
	meta, body, err := splitFrontmatter(data, path)
	if err != nil {
		return Skill{}, "", err
	}
	origin, err := filepath.Rel(root, filepath.Dir(path))
	if err != nil {
		return Skill{}, "", fmt.Errorf("importer: relative skill path: %w", err)
	}
	skill := Skill{Body: body, Origin: filepath.ToSlash(origin), Runtime: runtimeRefs(body), Data: append([]byte(nil), data...)}
	name := childString(meta, "name")
	if !validSkillName(name) {
		return skill, "SKILL.md has no valid frontmatter name", nil
	}
	skill.Name, skill.Description = name, childString(meta, "description")
	if skill.Description == "" || unsafeText(name) || unsafeText(skill.Description) {
		return skill, "SKILL.md description is missing or unsafe", nil
	}
	skill.SHA256 = sha256Hex(data)
	skill.setToS()
	return skill, "", nil
}

func splitFrontmatter(data []byte, path string) (map[string]any, string, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return nil, text, nil
	}
	head, tail, found := strings.Cut(rest, "\n---")
	if !found {
		return nil, "", syntaxError(path, errors.New("unterminated frontmatter"))
	}
	if strings.TrimSpace(head) == "" {
		return map[string]any{}, strings.TrimPrefix(tail, "\n"), nil
	}
	meta, ambiguous, err := decodeYAML11([]byte(head), path)
	if err != nil {
		return nil, "", err
	}
	if meta == nil {
		meta = map[string]any{}
	}
	for _, key := range []string{"name", "description"} {
		if _, ok := meta[key].(string); !ok {
			if raw, ok := ambiguous[key]; ok {
				meta[key] = raw
			}
		}
	}
	return meta, strings.TrimPrefix(tail, "\n"), nil
}
