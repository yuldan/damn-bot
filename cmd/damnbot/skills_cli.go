package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
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
	if len(args) < 2 || args[0] != "trust" {
		say(stderr, "pemakaian: damnbot skills trust list|add <dir>|remove <dir>\n")
		return 2
	}
	configDir, err := skillsConfigDir()
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

// skillsConfigDir resolves <config> the same way `damnbot run` does.
func skillsConfigDir() (string, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return "", fmt.Errorf("config deps: %w", err)
	}
	paths, err := loader.Paths("")
	if err != nil {
		return "", fmt.Errorf("config paths: %w", err)
	}
	return paths.ConfigDir, nil
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
