package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/damnbot/damnbot/internal/skills"
)

// paymentSkillDirs computes the shell-gate directories for the skills under
// <config>/skills (SPEC §4.8, SEC-15/SEC-26, ADR-133 amendment SEC10-6).
// Every skill directory is parsed and linted before use; a skill that cannot
// be read or is invalid fails the run instead of silently leaving its files
// ungated (fail closed). PaymentDirs covers declared payment, undeclared
// payment text, and skills that are not approved yet.
func paymentSkillDirs(configDir string) ([]string, error) {
	if configDir == "" {
		return nil, nil
	}
	list, err := loadSkills(filepath.Join(configDir, "skills"))
	if err != nil {
		return nil, err
	}
	return skills.PaymentDirs(list), nil
}

// loadSkills parses and lints every directory under root that has a
// SKILL.md. A missing root is no skills.
func loadSkills(root string) ([]skills.Skill, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("skills: %w", err)
	}
	var list []skills.Skill
	known := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sk, ok, lerr := loadSkill(root, e.Name())
		if lerr != nil {
			return nil, lerr
		}
		if !ok {
			continue
		}
		if known[sk.Name] {
			return nil, fmt.Errorf("%w: duplicate skill name %q", skills.ErrInvalid, sk.Name)
		}
		known[sk.Name] = true
		list = append(list, sk)
	}
	for i := range list {
		if err := checkLoaded(&list[i], known); err != nil {
			return nil, err
		}
	}
	return list, nil
}

// loadSkill parses one skill directory; ok is false when it is not a skill.
func loadSkill(root, name string) (sk skills.Skill, ok bool, err error) {
	p := filepath.Join(root, name, "SKILL.md")
	data, err := os.ReadFile(p) //nolint:gosec // fixed config path, not user input
	if errors.Is(err, fs.ErrNotExist) {
		return sk, false, nil
	}
	if err != nil {
		return sk, false, fmt.Errorf("skills: %s: %w", name, err)
	}
	sk, err = skills.Parse(data, p, skills.SourceUser)
	if err != nil {
		return sk, false, fmt.Errorf("skills: %s: %w", name, err)
	}
	return sk, true, nil
}

// checkLoaded lints one skill through os.Root and rejects a high finding,
// matching Service.Install for a trusted source.
func checkLoaded(sk *skills.Skill, known map[string]bool) error {
	root, err := os.OpenRoot(filepath.Dir(sk.Path))
	if err != nil {
		return fmt.Errorf("skills: %s: %w", sk.Name, err)
	}
	defer root.Close() //nolint:errcheck // read-only handle; a close error is not actionable here
	findings, err := skills.Lint(sk, skills.LintEnv{Files: root.FS(), Known: func(n string) bool { return known[n] }})
	if err != nil {
		return fmt.Errorf("skills: %s: %w", sk.Name, err)
	}
	for _, f := range findings {
		if f.Severity == "high" {
			return fmt.Errorf("%w: %s: %s", skills.ErrInvalid, sk.Name, f.Category)
		}
	}
	return nil
}
