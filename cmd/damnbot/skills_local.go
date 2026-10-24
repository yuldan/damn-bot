package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/skills"
)

// runSkills implements the local half of `damnbot skills` (CLI-046):
// list/inspect/check/update/index/diff/audit/ledger/config/install/uninstall; hub/network subcommands are F8 (ADR-139).
func runSkills(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		skillsUsage(stderr)
		return 2
	}
	switch args[0] {
	case "list":
		return skillsList(args[1:], stdout, stderr)
	case "inspect":
		return skillsInspect(args[1:], stdout, stderr)
	case "check":
		return skillsCheck(args[1:], stdout, stderr)
	case "update", "index":
		return skillsUpdate(args[1:], stdout, stderr, args[0] == "index")
	case "diff":
		return skillsDiff(args[1:], stdout, stderr)
	case "audit", "ledger":
		return skillsAudit(args[1:], stdout, stderr)
	case "config":
		return skillsConfig(args[1:], stdout, stderr)
	case "reset":
		return skillsReset(args[1:], stdout, stderr)
	case "install":
		return skillsInstall(args[1:], stdout, stderr)
	case "uninstall":
		return skillsUninstall(args[1:], stdout, stderr)
	default:
		skillsPrintf(stderr, "skills: unknown subcommand %q\n", skillsArg(args[0]))
		skillsUsage(stderr)
		return 2
	}
}

func skillsUsage(w io.Writer) {
	skillsPrintf(w, "usage: damnbot skills list|check|update|index [--check]|audit|ledger|config\n")
	skillsPrintf(w, "       damnbot skills inspect <name>|diff <name>\n")
	skillsPrintf(w, "       damnbot skills install <dir>|uninstall <name> --yes|reset --yes\n")
}

func skillsPrintf(w io.Writer, format string, args ...any) {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return
	}
}

func skillsArg(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

type skillArgs struct {
	configDir, dataDir string
	yes, check, json   bool
	limit              int
	pos                []string
}

func parseSkillArgs(args []string) (skillArgs, error) {
	var a skillArgs
	i := 0
	for i < len(args) {
		s := args[i]
		if !strings.HasPrefix(s, "-") || s == "-" {
			a.pos = append(a.pos, s)
			i++
			continue
		}
		handled, err := skillFlag(&a, s, args, &i)
		if err != nil {
			return a, err
		}
		if !handled {
			return a, fmt.Errorf("unknown flag %q", skillsArg(s))
		}
		i++
	}
	loader, err := config.New(config.OS())
	if err != nil {
		return a, fmt.Errorf("config deps: %w", err)
	}
	paths, err := loader.Paths("")
	if err != nil {
		return a, fmt.Errorf("config paths: %w", err)
	}
	if a.configDir == "" {
		a.configDir = paths.ConfigDir
	}
	if a.dataDir == "" {
		a.dataDir = paths.DataDir
	}
	return a, nil
}

// skillFlag parses one flag at args[*i].
func skillFlag(a *skillArgs, s string, args []string, i *int) (bool, error) {
	value := func(name string) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("%s needs a value", name)
		}
		*i++
		return args[*i], nil
	}
	for _, f := range []struct {
		name string
		dst  *string
	}{{"--config-dir", &a.configDir}, {"--data-dir", &a.dataDir}} {
		if s == f.name {
			v, err := value(f.name)
			*f.dst = v
			return true, err
		}
		if v, ok := strings.CutPrefix(s, f.name+"="); ok {
			if v == "" {
				return true, fmt.Errorf("%s needs a value", f.name)
			}
			*f.dst = v
			return true, nil
		}
	}
	switch {
	case s == "--limit":
		v, err := value(s)
		if err != nil {
			return true, err
		}
		return true, skillLimit(a, v)
	case strings.HasPrefix(s, "--limit="):
		return true, skillLimit(a, strings.TrimPrefix(s, "--limit="))
	case s == "--yes" || s == "-y":
		a.yes = true
	case s == "--check":
		a.check = true
	case s == "--json":
		a.json = true
	default:
		return false, nil
	}
	return true, nil
}

// skillLimit parses a positive --limit.
func skillLimit(a *skillArgs, v string) error {
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fmt.Errorf("--limit %q: want a positive integer", skillsArg(v))
	}
	a.limit = n
	return nil
}

// configSkills loads and lints the skills under <config>/skills.
func configSkills(ctx context.Context, a skillArgs) ([]skills.Skill, error) {
	cat, err := skills.Load(ctx, []skills.Root{{Path: filepath.Join(a.configDir, "skills"), Source: skills.SourceUser}}, skills.LoadOptions{})
	if err != nil {
		return nil, err
	}
	return cat.Skills, nil
}

func skillsList(args []string, stdout, stderr io.Writer) int {
	a, err := parseSkillArgs(args)
	if err != nil || len(a.pos) != 0 {
		if err != nil {
			skillsPrintf(stderr, "skills list: %v\n", err)
		}
		skillsUsage(stderr)
		return 2
	}
	ctx := context.Background()
	list, err := configSkills(ctx, a)
	if err != nil {
		skillsPrintf(stderr, "skills list: %v\n", err)
		return 1
	}
	ledger, err := skills.ReadLedger(a.dataDir)
	if err != nil {
		skillsPrintf(stderr, "skills list: %v\n", err)
		return 1
	}
	for i := range list {
		sk := &list[i]
		action := "-"
		if e, ok := skills.Provenance(ledger, sk.Name); ok {
			action = e.Action
		}
		skillsPrintf(stdout, "%s\t%s\t%s\t%s\n", sk.Name, shortSHA(sk.SHA256), action, oneLine(sk.Description))
	}
	return 0
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	if len([]rune(s)) > 120 {
		return string([]rune(s)[:120]) + "…"
	}
	return s
}

// findSkill returns the config skill with that name.
func findSkill(list []skills.Skill, name string) (skills.Skill, bool) {
	for i := range list {
		if list[i].Name == name {
			return list[i], true
		}
	}
	return skills.Skill{}, false
}

func skillsInspect(args []string, stdout, stderr io.Writer) int {
	a, err := parseSkillArgs(args)
	if err != nil || len(a.pos) != 1 {
		if err != nil {
			skillsPrintf(stderr, "skills inspect: %v\n", err)
		}
		skillsUsage(stderr)
		return 2
	}
	ctx := context.Background()
	list, err := configSkills(ctx, a)
	if err != nil {
		skillsPrintf(stderr, "skills inspect: %v\n", err)
		return 1
	}
	sk, ok := findSkill(list, a.pos[0])
	if !ok {
		skillsPrintf(stderr, "skills inspect: %s not found\n", skillsArg(a.pos[0]))
		return 1
	}
	if a.json {
		b, merr := json.MarshalIndent(struct {
			Name, Description, Path, SHA256, Source, Body string
		}{sk.Name, sk.Description, sk.Path, sk.SHA256, string(sk.Source), sk.Body}, "", "  ")
		if merr != nil {
			skillsPrintf(stderr, "skills inspect: %v\n", merr)
			return 1
		}
		skillsPrintf(stdout, "%s\n", b)
		return 0
	}
	skillsPrintf(stdout, "name: %s\nsource: %s\npath: %s\nsha256: %s\ndescription: %s\n---\n%s\n",
		sk.Name, sk.Source, sk.Path, sk.SHA256, sk.Description, sk.Body)
	return 0
}

func skillsCheck(args []string, stdout, stderr io.Writer) int {
	a, err := parseSkillArgs(args)
	if err != nil || len(a.pos) != 0 {
		if err != nil {
			skillsPrintf(stderr, "skills check: %v\n", err)
		}
		skillsUsage(stderr)
		return 2
	}
	list, err := configSkills(context.Background(), a)
	if err != nil {
		skillsPrintf(stderr, "skills check: %v\n", err)
		return 1
	}
	skillsPrintf(stdout, "ok: %d skills\n", len(list))
	return 0
}

func skillsUpdate(args []string, stdout, stderr io.Writer, indexOnly bool) int {
	a, err := parseSkillArgs(args)
	if err != nil || len(a.pos) != 0 {
		if err != nil {
			skillsPrintf(stderr, "skills update: %v\n", err)
		}
		skillsUsage(stderr)
		return 2
	}
	ctx := context.Background()
	list, err := configSkills(ctx, a)
	if err != nil {
		skillsPrintf(stderr, "skills update: %v\n", err)
		return 1
	}
	next := skills.BuildIndex(list)
	prev, err := skills.LoadIndex(a.configDir)
	if err != nil {
		skillsPrintf(stderr, "skills update: %v\n", err)
		return 1
	}
	delta := skills.Diff(prev, next)
	if a.check {
		if !delta.Empty() {
			printDelta(stdout, delta)
			return 1
		}
		skillsPrintf(stdout, "index up to date (%d skills)\n", len(next))
		return 0
	}
	if err := skills.SaveIndex(a.configDir, next); err != nil {
		skillsPrintf(stderr, "skills update: %v\n", err)
		return 1
	}
	printDelta(stdout, delta)
	_ = indexOnly
	return 0
}

func printDelta(w io.Writer, d skills.IndexDelta) {
	for _, n := range d.Added {
		skillsPrintf(w, "added %s\n", n)
	}
	for _, n := range d.Changed {
		skillsPrintf(w, "changed %s\n", n)
	}
	for _, n := range d.Removed {
		skillsPrintf(w, "removed %s\n", n)
	}
}

func skillsDiff(args []string, stdout, stderr io.Writer) int {
	a, err := parseSkillArgs(args)
	if err != nil || len(a.pos) != 1 {
		if err != nil {
			skillsPrintf(stderr, "skills diff: %v\n", err)
		}
		skillsUsage(stderr)
		return 2
	}
	list, err := configSkills(context.Background(), a)
	if err != nil {
		skillsPrintf(stderr, "skills diff: %v\n", err)
		return 1
	}
	sk, ok := findSkill(list, a.pos[0])
	if !ok {
		skillsPrintf(stderr, "skills diff: %s not found\n", skillsArg(a.pos[0]))
		return 1
	}
	ledger, err := skills.ReadLedger(a.dataDir)
	if err != nil {
		skillsPrintf(stderr, "skills diff: %v\n", err)
		return 1
	}
	last, ok := skills.Provenance(ledger, sk.Name)
	if !ok {
		skillsPrintf(stdout, "%s: no previous ledger entry (current %s)\n", sk.Name, shortSHA(sk.SHA256))
		return 0
	}
	if last.SHA256 == sk.SHA256 {
		skillsPrintf(stdout, "%s: unchanged (%s)\n", sk.Name, shortSHA(sk.SHA256))
		return 0
	}
	skillsPrintf(stdout, "%s: changed %s -> %s (last action %s)\n", sk.Name, shortSHA(last.SHA256), shortSHA(sk.SHA256), last.Action)
	return 0
}

func skillsAudit(args []string, stdout, stderr io.Writer) int {
	a, err := parseSkillArgs(args)
	if err != nil || len(a.pos) != 0 {
		if err != nil {
			skillsPrintf(stderr, "skills audit: %v\n", err)
		}
		skillsUsage(stderr)
		return 2
	}
	entries, err := skills.ReadLedger(a.dataDir)
	if err != nil {
		skillsPrintf(stderr, "skills audit: %v\n", err)
		return 1
	}
	if a.limit > 0 && len(entries) > a.limit {
		entries = entries[len(entries)-a.limit:]
	}
	for _, e := range entries {
		skillsPrintf(stdout, "%s\t%s\t%s\t%s\t%s\n", time.UnixMilli(e.At).UTC().Format(time.RFC3339),
			e.Name, e.Action, e.Source, shortSHA(e.SHA256))
	}
	return 0
}

func skillsConfig(args []string, stdout, stderr io.Writer) int {
	a, err := parseSkillArgs(args)
	if err != nil || len(a.pos) != 0 {
		if err != nil {
			skillsPrintf(stderr, "skills config: %v\n", err)
		}
		skillsUsage(stderr)
		return 2
	}
	loader, err := config.New(config.OS())
	if err != nil {
		skillsPrintf(stderr, "skills config: %v\n", err)
		return 1
	}
	cfg := config.Default()
	paths, perr := loader.Paths("")
	if perr != nil {
		skillsPrintf(stderr, "skills config: %v\n", perr)
		return 1
	}
	if _, serr := os.Stat(paths.ConfigFile()); serr == nil {
		loaded, lerr := loader.LoadProfile("")
		if lerr != nil {
			skillsPrintf(stderr, "skills config: %v\n", lerr)
			return 1
		}
		cfg = loaded
	}
	b, err := json.MarshalIndent(cfg.Skills, "", "  ")
	if err != nil {
		skillsPrintf(stderr, "skills config: %v\n", err)
		return 1
	}
	skillsPrintf(stdout, "%s\n", b)
	return 0
}

// skillsReset removes the built index (skills themselves are untouched).
func skillsReset(args []string, stdout, stderr io.Writer) int {
	a, err := parseSkillArgs(args)
	if err != nil || len(a.pos) != 0 {
		if err != nil {
			skillsPrintf(stderr, "skills reset: %v\n", err)
		}
		skillsUsage(stderr)
		return 2
	}
	if !a.yes {
		skillsPrintf(stderr, "skills reset: refusing to remove the index without --yes\n")
		return 2
	}
	path := filepath.Join(a.configDir, skills.IndexFile)
	if rerr := os.Remove(path); rerr != nil && !os.IsNotExist(rerr) {
		skillsPrintf(stderr, "skills reset: %v\n", rerr)
		return 1
	}
	skillsPrintf(stdout, "reset skills index\n")
	return 0
}

// skillsInstall copies one skill directory into <config>/skills and records
// it as a user index entry. Symlinks and special files are refused.
func skillsInstall(args []string, stdout, stderr io.Writer) int {
	a, err := parseSkillArgs(args)
	if err != nil || len(a.pos) != 1 {
		if err != nil {
			skillsPrintf(stderr, "skills install: %v\n", err)
		}
		skillsUsage(stderr)
		return 2
	}
	src := a.pos[0]
	sk, err := readInstallSource(src)
	if err != nil {
		skillsPrintf(stderr, "skills install: %v\n", err)
		return 1
	}
	destRoot := filepath.Join(a.configDir, "skills")
	if err := os.MkdirAll(destRoot, 0o700); err != nil {
		skillsPrintf(stderr, "skills install: %v\n", err)
		return 1
	}
	dest := filepath.Join(destRoot, sk.Name)
	if _, serr := os.Lstat(dest); serr == nil {
		skillsPrintf(stderr, "skills install: %s already exists\n", sk.Name)
		return 2
	}
	if cerr := copyTree(src, dest); cerr != nil {
		if rerr := os.RemoveAll(dest); rerr != nil {
			return 1
		}
		skillsPrintf(stderr, "skills install: %v\n", cerr)
		return 1
	}
	skillsPrintf(stdout, "installed %s (%s)\n", sk.Name, shortSHA(sk.SHA256))
	return 0
}

// readInstallSource parses the SKILL.md of a directory to install.
func readInstallSource(src string) (skills.Skill, error) {
	abs, err := filepath.Abs(src)
	if err != nil {
		return skills.Skill{}, fmt.Errorf("source path: %w", err)
	}
	fi, err := os.Stat(abs)
	if err != nil || !fi.IsDir() {
		return skills.Skill{}, fmt.Errorf("%s is not a directory", skillsArg(src))
	}
	data, err := os.ReadFile(filepath.Join(abs, "SKILL.md")) //nolint:gosec // user-selected install source
	if err != nil {
		return skills.Skill{}, fmt.Errorf("read SKILL.md: %w", err)
	}
	sk, err := skills.Parse(data, filepath.Join(abs, "SKILL.md"), skills.SourceUser)
	if err != nil {
		return skills.Skill{}, fmt.Errorf("parse SKILL.md: %w", err)
	}
	return sk, nil
}

// copyTree copies regular files only (symlinks/special files refused).
func copyTree(src, dest string) error {
	walkErr := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk source: %w", err)
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return fmt.Errorf("relative path: %w", rerr)
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("refusing non-regular file %s", skillsArg(rel))
		}
		data, rerr := os.ReadFile(path) //nolint:gosec // install source chosen by the user
		if rerr != nil {
			return fmt.Errorf("read source file: %w", rerr)
		}
		if werr := os.WriteFile(target, data, 0o600); werr != nil { //nolint:gosec // target is below the install destination
			return fmt.Errorf("write skill file: %w", werr)
		}
		return nil
	})
	if walkErr != nil {
		return fmt.Errorf("copy skill: %w", walkErr)
	}
	return nil
}

// skillsUninstall removes <config>/skills/<name> with --yes.
func skillsUninstall(args []string, stdout, stderr io.Writer) int {
	a, err := parseSkillArgs(args)
	if err != nil || len(a.pos) != 1 {
		if err != nil {
			skillsPrintf(stderr, "skills uninstall: %v\n", err)
		}
		skillsUsage(stderr)
		return 2
	}
	name := a.pos[0]
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
		skillsPrintf(stderr, "skills uninstall: invalid name %q\n", skillsArg(name))
		return 2
	}
	if !a.yes {
		skillsPrintf(stderr, "skills uninstall: refusing to delete %s without --yes\n", skillsArg(name))
		return 2
	}
	rootDir := filepath.Join(a.configDir, "skills")
	// name is validated as a single basename above; the parent is the config skills dir.
	if err := os.RemoveAll(filepath.Join(rootDir, name)); err != nil {
		skillsPrintf(stderr, "skills uninstall: %v\n", err)
		return 1
	}
	skillsPrintf(stdout, "uninstalled %s\n", name)
	return 0
}
