package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/skills"
)

// say writes one line to w and reports a failed write as exit code 1. The
// helpers below keep every output checked without spreading errcheck
// boilerplate over the usage paths.
func say(w io.Writer, format string, args ...any) int {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return 1
	}
	return 0
}

// skillsCommand implements the local subset of `damnbot skills` (CLI-046):
// `damnbot skills trust list|add <dir>|remove <dir>` manages
// <config>/trusted-projects, the only way a project becomes trusted for skill
// discovery (ADR-175). Nothing outside that file is read: add/remove only
// stat the directory they are given.
func skillsCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("damnbot skills", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file; its directory holds trusted-projects")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	args = fs.Args()
	if len(args) == 0 {
		skillsUsage(stderr)
		return 2
	}
	if args[0] != "trust" {
		// One entry point: the local skills subcommands (ADR-172) live in
		// skills_local.go and share this dispatch with `skills trust`.
		return runSkills(args, stdout, stderr)
	}
	if len(args) < 2 {
		say(stderr, "pemakaian: damnbot skills trust list|add <dir>|remove <dir>\n")
		return 2
	}
	configDir, err := skillsConfigDir(*cfgPath)
	if err != nil {
		say(stderr, "damnbot skills: %v\n", err)
		return 1
	}
	switch args[1] {
	case "list":
		if len(args) != 2 {
			say(stderr, "pemakaian: damnbot skills trust list\n")
			return 2
		}
		return skillsTrustList(configDir, stdout)
	case "add", "remove":
		if len(args) != 3 {
			say(stderr, "pemakaian: damnbot skills trust %s <dir>\n", args[1])
			return 2
		}
		return skillsTrustEdit(configDir, args[1], args[2], stdout, stderr)
	default:
		say(stderr, "damnbot skills trust: subcommand %q tidak dikenal\n", args[1])
		return 2
	}
}

// skillsPaths resolves the config and data directories the way `damnbot run`
// does; the skills CLI is the only caller that needs both at once.
func skillsPaths() (configDir, dataDir string, err error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return "", "", fmt.Errorf("config deps: %w", err)
	}
	paths, err := loader.Paths("")
	if err != nil {
		return "", "", fmt.Errorf("config paths: %w", err)
	}
	return paths.ConfigDir, paths.DataDir, nil
}

// skillsConfigDir resolves <config>. An explicit --config file wins (its
// directory is the store location), otherwise the loader decides the way
// `damnbot run` does.
func skillsConfigDir(cfgPath string) (string, error) {
	if cfgPath != "" {
		abs, err := filepath.Abs(cfgPath)
		if err != nil {
			return "", fmt.Errorf("config path: %w", err)
		}
		return filepath.Dir(abs), nil
	}
	dir, _, err := skillsPaths()
	return dir, err
}

func skillsTrustList(configDir string, stdout io.Writer) int {
	list, err := trustedList(configDir)
	if err != nil {
		say(os.Stderr, "damnbot skills trust list: %v\n", err)
		return 1
	}
	for _, p := range list {
		if say(stdout, "%s\n", p) != 0 {
			return 1
		}
	}
	return 0
}

// skillsTrustEdit adds or removes one trusted project. The path is normalized
// before any write: it must be an existing directory and may not be the
// config directory itself, so `add` can never be satisfied by a typo.
func skillsTrustEdit(configDir, action, raw string, stdout, stderr io.Writer) int {
	path, err := normalizeTrustedProject(raw)
	if err != nil {
		say(stderr, "damnbot skills trust %s: %v\n", action, err)
		return 1
	}
	list, err := trustedList(configDir)
	if err != nil {
		say(stderr, "damnbot skills trust: %v\n", err)
		return 1
	}
	updated := slices.Clone(list)
	switch action {
	case "add":
		if slices.Contains(list, path) {
			say(stdout, "sudah tepercaya: %s\n", path)
			return 0
		}
		updated = append(updated, path)
	case "remove":
		i := slices.Index(list, path)
		if i < 0 {
			say(stderr, "damnbot skills trust remove: %s tidak ada di daftar tepercaya\n", path)
			return 1
		}
		updated = slices.Delete(list, i, i+1)
	}
	if werr := writeTrustedFile(configDir, updated); werr != nil {
		say(stderr, "damnbot skills trust: %v\n", werr)
		return 1
	}
	say(stdout, "%s: %s\n", action, path)
	return 0
}

// trustedList reads the store and canonicalizes every entry, so a hand-edited
// line compares equal to the path `add` normalizes.
func trustedList(configDir string) ([]string, error) {
	list, err := readTrustedFile(configDir)
	if err != nil {
		return nil, err
	}
	home, herr := os.UserHomeDir()
	if herr != nil {
		return nil, fmt.Errorf("home: %w", herr)
	}
	out := make([]string, 0, len(list))
	for _, p := range list {
		c, cerr := canonicalPath(p, home)
		if cerr != nil {
			c = p // keep an unreadable entry visible instead of hiding the trust
		}
		out = append(out, c)
	}
	return out, nil
}

// normalizeTrustedProject canonicalizes a project path: non-empty, absolute
// after expanding ~, an existing directory, and not the config directory.
func normalizeTrustedProject(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("path kosong")
	}
	home, herr := os.UserHomeDir()
	if herr != nil {
		return "", fmt.Errorf("home: %w", herr)
	}
	p, err := canonicalPath(raw, home)
	if err != nil {
		return "", err
	}
	root, oerr := os.OpenRoot(filepath.Dir(p))
	if oerr != nil {
		return "", fmt.Errorf("%s: %w", p, oerr)
	}
	info, serr := root.Stat(filepath.Base(p))
	if cerr := root.Close(); cerr != nil {
		return "", fmt.Errorf("%s: %w", p, cerr)
	}
	if errors.Is(serr, fs.ErrNotExist) {
		return "", fmt.Errorf("%s tidak ada", p)
	}
	if serr != nil {
		return "", fmt.Errorf("%s: %w", p, serr)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s bukan direktori", p)
	}
	return p, nil
}

// writeTrustedFile rewrites the trust store atomically (temp + rename) with
// one absolute path per line.
func writeTrustedFile(configDir string, list []string) error {
	if configDir == "" {
		return errors.New("config dir tidak diketahui")
	}
	sorted := slices.Clone(list)
	slices.Sort(sorted)
	sorted = slices.Compact(sorted)
	body := strings.Join(sorted, "\n")
	if body != "" {
		body += "\n"
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return fmt.Errorf("config dir: %w", err)
	}
	root, err := os.OpenRoot(configDir)
	if err != nil {
		return fmt.Errorf("config dir: %w", err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			_ = cerr // the write is already committed; a close error is not actionable
		}
	}()
	tmp := trustedProjectsFile + ".tmp"
	if err := root.WriteFile(tmp, []byte(body), 0o600); err != nil {
		return fmt.Errorf("tulis %s: %w", trustedProjectsFile, err)
	}
	if err := root.Rename(tmp, trustedProjectsFile); err != nil {
		return fmt.Errorf("simpan %s: %w", trustedProjectsFile, err)
	}
	return nil
}

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
		say(stderr, "skills: unknown subcommand %q\n", skillsArg(args[0]))
		skillsUsage(stderr)
		return 2
	}
}

func skillsUsage(w io.Writer) {
	say(w, "usage: damnbot skills list|check|update|index [--check]|audit|ledger|config\n")
	say(w, "       damnbot skills inspect <name>|diff <name>\n")
	say(w, "       damnbot skills install <dir>|uninstall <name> --yes|reset --yes\n")
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
	cfgDir, dataDir, perr := skillsPaths()
	if perr != nil {
		return a, perr
	}
	if a.configDir == "" {
		a.configDir = cfgDir
	}
	if a.dataDir == "" {
		a.dataDir = dataDir
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
		return nil, fmt.Errorf("load skills: %w", err)
	}
	return cat.Skills, nil
}

func skillsList(args []string, stdout, stderr io.Writer) int {
	a, err := parseSkillArgs(args)
	if err != nil || len(a.pos) != 0 {
		if err != nil {
			say(stderr, "skills list: %v\n", err)
		}
		skillsUsage(stderr)
		return 2
	}
	ctx := context.Background()
	list, err := configSkills(ctx, a)
	if err != nil {
		say(stderr, "skills list: %v\n", err)
		return 1
	}
	ledger, err := skills.ReadLedger(a.dataDir)
	if err != nil {
		say(stderr, "skills list: %v\n", err)
		return 1
	}
	for i := range list {
		sk := &list[i]
		action := "-"
		if e, ok := skills.Provenance(ledger, sk.Name); ok {
			action = e.Action
		}
		say(stdout, "%s\t%s\t%s\t%s\n", sk.Name, shortSHA(sk.SHA256), action, strings.Join(strings.Fields(strings.ReplaceAll(strings.ReplaceAll(sk.Description, "\n", " "), "\r", " ")), " "))
	}
	return 0
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
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
			say(stderr, "skills inspect: %v\n", err)
		}
		skillsUsage(stderr)
		return 2
	}
	ctx := context.Background()
	list, err := configSkills(ctx, a)
	if err != nil {
		say(stderr, "skills inspect: %v\n", err)
		return 1
	}
	sk, ok := findSkill(list, a.pos[0])
	if !ok {
		say(stderr, "skills inspect: %s not found\n", skillsArg(a.pos[0]))
		return 1
	}
	if a.json {
		b, merr := json.MarshalIndent(struct {
			Name, Description, Path, SHA256, Source, Body string
		}{sk.Name, sk.Description, sk.Path, sk.SHA256, string(sk.Source), sk.Body}, "", "  ")
		if merr != nil {
			say(stderr, "skills inspect: %v\n", merr)
			return 1
		}
		say(stdout, "%s\n", b)
		return 0
	}
	say(stdout, "name: %s\nsource: %s\npath: %s\nsha256: %s\ndescription: %s\n---\n%s\n",
		sk.Name, sk.Source, sk.Path, sk.SHA256, sk.Description, sk.Body)
	return 0
}

func skillsCheck(args []string, stdout, stderr io.Writer) int {
	a, err := parseSkillArgs(args)
	if err != nil || len(a.pos) != 0 {
		if err != nil {
			say(stderr, "skills check: %v\n", err)
		}
		skillsUsage(stderr)
		return 2
	}
	list, err := configSkills(context.Background(), a)
	if err != nil {
		say(stderr, "skills check: %v\n", err)
		return 1
	}
	say(stdout, "ok: %d skills\n", len(list))
	return 0
}

func skillsUpdate(args []string, stdout, stderr io.Writer, indexOnly bool) int {
	a, err := parseSkillArgs(args)
	if err != nil || len(a.pos) != 0 {
		if err != nil {
			say(stderr, "skills update: %v\n", err)
		}
		skillsUsage(stderr)
		return 2
	}
	ctx := context.Background()
	list, err := configSkills(ctx, a)
	if err != nil {
		say(stderr, "skills update: %v\n", err)
		return 1
	}
	next := skills.BuildIndex(list)
	prev, err := skills.LoadIndex(a.configDir)
	if err != nil {
		say(stderr, "skills update: %v\n", err)
		return 1
	}
	delta := skills.Diff(prev, next)
	if a.check {
		if !delta.Empty() {
			printDelta(stdout, delta)
			return 1
		}
		say(stdout, "index up to date (%d skills)\n", len(next))
		return 0
	}
	if err := skills.SaveIndex(a.configDir, next); err != nil {
		say(stderr, "skills update: %v\n", err)
		return 1
	}
	printDelta(stdout, delta)
	_ = indexOnly
	return 0
}

func printDelta(w io.Writer, d skills.IndexDelta) {
	for _, n := range d.Added {
		say(w, "added %s\n", n)
	}
	for _, n := range d.Changed {
		say(w, "changed %s\n", n)
	}
	for _, n := range d.Removed {
		say(w, "removed %s\n", n)
	}
}

func skillsDiff(args []string, stdout, stderr io.Writer) int {
	a, err := parseSkillArgs(args)
	if err != nil || len(a.pos) != 1 {
		if err != nil {
			say(stderr, "skills diff: %v\n", err)
		}
		skillsUsage(stderr)
		return 2
	}
	list, err := configSkills(context.Background(), a)
	if err != nil {
		say(stderr, "skills diff: %v\n", err)
		return 1
	}
	sk, ok := findSkill(list, a.pos[0])
	if !ok {
		say(stderr, "skills diff: %s not found\n", skillsArg(a.pos[0]))
		return 1
	}
	ledger, err := skills.ReadLedger(a.dataDir)
	if err != nil {
		say(stderr, "skills diff: %v\n", err)
		return 1
	}
	last, ok := skills.Provenance(ledger, sk.Name)
	if !ok {
		say(stdout, "%s: no previous ledger entry (current %s)\n", sk.Name, shortSHA(sk.SHA256))
		return 0
	}
	if last.SHA256 == sk.SHA256 {
		say(stdout, "%s: unchanged (%s)\n", sk.Name, shortSHA(sk.SHA256))
		return 0
	}
	say(stdout, "%s: changed %s -> %s (last action %s)\n", sk.Name, shortSHA(last.SHA256), shortSHA(sk.SHA256), last.Action)
	return 0
}

func skillsAudit(args []string, stdout, stderr io.Writer) int {
	a, err := parseSkillArgs(args)
	if err != nil || len(a.pos) != 0 {
		if err != nil {
			say(stderr, "skills audit: %v\n", err)
		}
		skillsUsage(stderr)
		return 2
	}
	entries, err := skills.ReadLedger(a.dataDir)
	if err != nil {
		say(stderr, "skills audit: %v\n", err)
		return 1
	}
	if a.limit > 0 && len(entries) > a.limit {
		entries = entries[len(entries)-a.limit:]
	}
	for _, e := range entries {
		say(stdout, "%s\t%s\t%s\t%s\t%s\n", time.UnixMilli(e.At).UTC().Format(time.RFC3339),
			e.Name, e.Action, e.Source, shortSHA(e.SHA256))
	}
	return 0
}

// skillsReset removes the built index (skills themselves are untouched).
func skillsReset(args []string, stdout, stderr io.Writer) int {
	a, err := parseSkillArgs(args)
	if err != nil || len(a.pos) != 0 {
		if err != nil {
			say(stderr, "skills reset: %v\n", err)
		}
		skillsUsage(stderr)
		return 2
	}
	if !a.yes {
		say(stderr, "skills reset: refusing to remove the index without --yes\n")
		return 2
	}
	path := filepath.Join(a.configDir, skills.IndexFile)
	if rerr := os.Remove(path); rerr != nil && !os.IsNotExist(rerr) {
		say(stderr, "skills reset: %v\n", rerr)
		return 1
	}
	say(stdout, "reset skills index\n")
	return 0
}

// skillsInstall copies one skill directory into <config>/skills and records
// it as a user index entry. Symlinks and special files are refused.
func skillsInstall(args []string, stdout, stderr io.Writer) int {
	a, err := parseSkillArgs(args)
	if err != nil || len(a.pos) != 1 {
		if err != nil {
			say(stderr, "skills install: %v\n", err)
		}
		skillsUsage(stderr)
		return 2
	}
	src := a.pos[0]
	sk, err := readInstallSource(src)
	if err != nil {
		say(stderr, "skills install: %v\n", err)
		return 1
	}
	destRoot := filepath.Join(a.configDir, "skills")
	if err := os.MkdirAll(destRoot, 0o700); err != nil {
		say(stderr, "skills install: %v\n", err)
		return 1
	}
	dest := filepath.Join(destRoot, sk.Name)
	if _, serr := os.Lstat(dest); serr == nil {
		say(stderr, "skills install: %s already exists\n", sk.Name)
		return 2
	}
	if cerr := copyTree(src, dest); cerr != nil {
		if rerr := os.RemoveAll(dest); rerr != nil {
			return 1
		}
		say(stderr, "skills install: %v\n", cerr)
		return 1
	}
	say(stdout, "installed %s (%s)\n", sk.Name, shortSHA(sk.SHA256))
	return 0
}

// readInstallSource parses the SKILL.md of a directory to install.
func readInstallSource(src string) (sk skills.Skill, err error) {
	abs, err := filepath.Abs(src)
	if err != nil {
		return skills.Skill{}, fmt.Errorf("source path: %w", err)
	}
	fi, err := os.Stat(abs)
	if err != nil || !fi.IsDir() {
		return skills.Skill{}, fmt.Errorf("%s is not a directory", skillsArg(src))
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return skills.Skill{}, fmt.Errorf("open source: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	data, err := root.ReadFile("SKILL.md")
	if err != nil {
		return skills.Skill{}, fmt.Errorf("read SKILL.md: %w", err)
	}
	sk, err = skills.Parse(data, filepath.Join(abs, "SKILL.md"), skills.SourceUser)
	if err != nil {
		return skills.Skill{}, fmt.Errorf("parse SKILL.md: %w", err)
	}
	return sk, nil
}

// copyTree copies regular files only (symlinks/special files refused); both sides
// are opened as os.Root so no path can escape the source or the destination.
func copyTree(src, dest string) (err error) {
	srcRoot, err := os.OpenRoot(src)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer func() { err = errors.Join(err, srcRoot.Close()) }()
	if merr := os.MkdirAll(dest, 0o700); merr != nil {
		return fmt.Errorf("create destination: %w", merr)
	}
	dstRoot, err := os.OpenRoot(dest)
	if err != nil {
		return fmt.Errorf("open destination: %w", err)
	}
	defer func() { err = errors.Join(err, dstRoot.Close()) }()
	walkErr := fs.WalkDir(srcRoot.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk source: %w", err)
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			return dstRoot.MkdirAll(rel, 0o700)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("refusing non-regular file %s", skillsArg(rel))
		}
		data, rerr := srcRoot.ReadFile(rel)
		if rerr != nil {
			return fmt.Errorf("read source file: %w", rerr)
		}
		if werr := dstRoot.WriteFile(rel, data, 0o600); werr != nil {
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
			say(stderr, "skills uninstall: %v\n", err)
		}
		skillsUsage(stderr)
		return 2
	}
	name := a.pos[0]
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." {
		say(stderr, "skills uninstall: invalid name %q\n", skillsArg(name))
		return 2
	}
	if !a.yes {
		say(stderr, "skills uninstall: refusing to delete %s without --yes\n", skillsArg(name))
		return 2
	}
	rootDir := filepath.Join(a.configDir, "skills")
	// name is validated as a single basename above; the parent is the config skills dir.
	if err := os.RemoveAll(filepath.Join(rootDir, name)); err != nil {
		say(stderr, "skills uninstall: %v\n", err)
		return 1
	}
	say(stdout, "uninstalled %s\n", name)
	return 0
}
