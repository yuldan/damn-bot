package importer

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

func (i *Importer) collectSkills(root string, c *collected) error {
	dir := filepath.Join(root, "skills")
	err := i.deps.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}
		data, err := i.readOptional(path)
		if err != nil {
			return err
		}
		meta, body, err := splitFrontmatter(data, path)
		if err != nil {
			return err
		}
		origin, relErr := filepath.Rel(root, filepath.Dir(path))
		if relErr != nil {
			return fmt.Errorf("importer: relative skill path: %w", relErr)
		}
		name := childString(meta, "name")
		if name == "" {
			c.report.skip(SourceSkills, filepath.ToSlash(origin), "SKILL.md has no frontmatter name")
			return nil
		}
		c.skills = append(c.skills, Skill{
			Name:        name,
			Description: childString(meta, "description"),
			Body:        body,
			Origin:      filepath.ToSlash(origin),
			Status:      "pending",
		})
		c.report.item(SourceSkills)
		return nil
	})
	if err != nil {
		return err
	}
	return nil
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
	meta := map[string]any{}
	if err := yaml.Unmarshal([]byte(head), &meta); err != nil {
		return nil, "", syntaxError(path, err)
	}
	return meta, strings.TrimPrefix(tail, "\n"), nil
}
