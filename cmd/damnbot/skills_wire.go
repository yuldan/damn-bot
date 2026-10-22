package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/damnbot/damnbot/internal/skills"
)

// paymentSkillDirs computes the shell-gate directories for the skills under
// <config>/skills (SPEC §4.8, SEC-15/SEC-26, ADR-133 amendment). Every skill
// directory is parsed and linted before use; a symlinked skill is resolved
// and both names are gated; a directory without SKILL.md is gated as well. A
// skill that cannot be read or is invalid fails the run instead of silently
// leaving its files ungated (fail closed).
func paymentSkillDirs(ctx context.Context, configDir string, opts skillLoadOptions) ([]string, error) {
	if configDir == "" {
		return nil, nil
	}
	list, extra, err := loadSkills(ctx, filepath.Join(configDir, "skills"), opts)
	if err != nil {
		return nil, err
	}
	return dedupeDirs(append(skills.PaymentDirs(list), extra...)), nil
}

// dedupeDirs removes duplicate gate directories in first-seen order.
func dedupeDirs(dirs []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// loadSkills parses and lints every directory under root. extra holds
// directories that are not skills (no SKILL.md) and are gated anyway.
type skillLoadOptions struct {
	InlineShell bool
	ProjectDir  string
	Home        string
	Source      skills.Source // empty = user (config skills)
}

func loadSkills(ctx context.Context, root string, opts skillLoadOptions) (list []skills.Skill, extra []string, err error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("skills: %w", err)
	}
	known := map[string]bool{}
	for _, e := range entries {
		p := filepath.Join(root, e.Name())
		info, serr := os.Stat(p) // follows a symlinked skill directory (SEC10-10)
		if serr != nil {
			return nil, nil, fmt.Errorf("skills: %s: %w", e.Name(), serr)
		}
		if !info.IsDir() {
			continue
		}
		sk, ok, lerr := loadSkill(root, e.Name(), opts.Source)
		if lerr != nil {
			return nil, nil, lerr
		}
		if !ok {
			extra = append(extra, p)
			continue
		}
		if known[sk.Name] {
			return nil, nil, fmt.Errorf("%w: duplicate skill name %q", skills.ErrInvalid, sk.Name)
		}
		known[sk.Name] = true
		if perr := preprocessLoaded(ctx, &sk, opts); perr != nil {
			return nil, nil, perr
		}
		list = append(list, sk)
	}
	for i := range list {
		if err := checkLoaded(&list[i], known); err != nil {
			return nil, nil, err
		}
	}
	return list, extra, nil
}

// loadSkill parses one skill directory; ok is false when it is not a skill.
func loadSkill(root, name string, source skills.Source) (sk skills.Skill, ok bool, err error) {
	dir := filepath.Join(root, name)
	p := filepath.Join(dir, "SKILL.md")
	r, err := os.OpenRoot(dir) // SEC10-12: SKILL.md cannot symlink outside
	if errors.Is(err, fs.ErrNotExist) {
		return sk, false, nil
	}
	if err != nil {
		return sk, false, fmt.Errorf("skills: %s: %w", name, err)
	}
	data, rerr := r.ReadFile("SKILL.md")
	if cerr := r.Close(); cerr != nil {
		return sk, false, fmt.Errorf("skills: %s: %w", name, cerr)
	}
	if errors.Is(rerr, fs.ErrNotExist) {
		return sk, false, nil
	}
	if rerr != nil {
		return sk, false, fmt.Errorf("skills: %s: %w", name, rerr)
	}
	if source == "" {
		source = skills.SourceUser
	}
	sk, err = skills.Parse(data, p, source)
	if err != nil {
		return sk, false, fmt.Errorf("skills: %s: %w", name, err)
	}
	return sk, true, nil
}

// preprocessLoaded expands SKILL-004 templates after lint; inline shell runs
// only for trusted sources and only when the config enables it.
func preprocessLoaded(ctx context.Context, sk *skills.Skill, opts skillLoadOptions) error {
	var shell skills.ShellRunner
	if opts.InlineShell && sk.Source.Trusted() {
		shell = skillShell{}
	}
	body, err := skills.Preprocess(ctx, sk.Body, skills.PreprocessVars{
		SkillDir:   filepath.Dir(sk.Path),
		SkillName:  sk.Name,
		ProjectDir: opts.ProjectDir,
		Home:       opts.Home,
	}, shell)
	if err != nil {
		return fmt.Errorf("skills: %s: %w", sk.Name, err)
	}
	sk.Body = body
	return nil
}

// skillShell runs inline skill commands with a hard timeout and a bounded
// output buffer.
type skillShell struct{}

func (skillShell) Run(ctx context.Context, dir, command string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	name, args := "sh", []string{"-c", command}
	if runtime.GOOS == "windows" {
		name, args = "cmd", []string{"/C", command}
	}
	cmd := exec.CommandContext(cctx, name, args...) //nolint:gosec // explicit opt-in feature for trusted skills
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &capWriter{w: &out}, &capWriter{w: &out}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("inline shell: %w", err)
	}
	return out.String(), nil
}

// capWriter keeps at most skills.MaxInlineOutput bytes and discards the rest.
type capWriter struct{ w *bytes.Buffer }

func (c *capWriter) Write(p []byte) (int, error) {
	if room := skills.MaxInlineOutput - c.w.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		_, _ = c.w.Write(p)
	}
	return len(p), nil
}

// checkLoaded lints one skill through os.Root, records file-level payment
// findings on the skill (SEC10-9) and rejects a high finding, matching
// Service.Install for a trusted source.
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
	sk.AddFindings(findings)
	for _, f := range findings {
		if f.Severity == "high" {
			return fmt.Errorf("%w: %s: %s", skills.ErrInvalid, sk.Name, f.Category)
		}
	}
	return nil
}
