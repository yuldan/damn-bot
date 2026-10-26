package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/skills"
)

// runBundles implements `damnbot bundles` (SKILL-007, CLI-047): skill bundles
// live in <config>/bundles.json as alias -> skill list.
func runBundles(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		bundlesUsage(stderr)
		return 2
	}
	switch args[0] {
	case "list":
		return bundlesList(args[1:], stdout, stderr)
	case "show":
		return bundlesShow(args[1:], stdout, stderr)
	case "create":
		return bundlesCreate(args[1:], stdout, stderr)
	case "delete":
		return bundlesDelete(args[1:], stdout, stderr)
	case "reload":
		return bundlesReload(args[1:], stdout, stderr)
	default:
		bundlesPrintf(stderr, "bundles: unknown subcommand %q\n", bundleArg(args[0]))
		bundlesUsage(stderr)
		return 2
	}
}

func bundlesUsage(w io.Writer) {
	bundlesPrintf(w, "usage: damnbot bundles list|reload\n")
	bundlesPrintf(w, "       damnbot bundles show <name>\n")
	bundlesPrintf(w, "       damnbot bundles create <name> <skill>...\n")
	bundlesPrintf(w, "       damnbot bundles delete <name> --yes\n")
}

func bundlesPrintf(w io.Writer, format string, args ...any) {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return
	}
}

// bundleArg strips control characters and bounds one echoed argument.
func bundleArg(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

type bundleArgs struct {
	configDir, dir string
	yes            bool
	pos            []string
}

func parseBundleArgs(args []string) (bundleArgs, error) {
	var a bundleArgs
	i := 0
	for i < len(args) {
		s := args[i]
		switch {
		case s == "--config-dir":
			if i+1 >= len(args) {
				return a, fmt.Errorf("--config-dir needs a value")
			}
			i++
			a.configDir = args[i]
		case strings.HasPrefix(s, "--config-dir="):
			a.configDir = strings.TrimPrefix(s, "--config-dir=")
		case s == "--yes" || s == "-y":
			a.yes = true
		case strings.HasPrefix(s, "-") && s != "-":
			return a, fmt.Errorf("unknown flag %q", bundleArg(s))
		default:
			a.pos = append(a.pos, s)
		}
		i++
	}
	if a.configDir == "" {
		dir, err := defaultConfigDir()
		if err != nil {
			return a, err
		}
		a.configDir = dir
	}
	a.dir = filepath.Join(a.configDir, "skills")
	return a, nil
}

// defaultConfigDir resolves the profile config directory.
func defaultConfigDir() (string, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return "", fmt.Errorf("config deps: %w", err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return "", fmt.Errorf("config paths: %w", err)
	}
	return paths.ConfigDir, nil
}

// bundleKnown returns the installed skill names under the config skills dir.
func bundleKnown(ctx context.Context, a bundleArgs) (map[string]bool, error) {
	cat, err := skills.Load(ctx, []skills.Root{{Path: a.dir, Source: skills.SourceUser}}, skills.LoadOptions{})
	if err != nil {
		return nil, fmt.Errorf("load skills: %w", err)
	}
	known := make(map[string]bool, len(cat.Skills))
	for i := range cat.Skills {
		known[cat.Skills[i].Name] = true
	}
	return known, nil
}

func bundleFile(a bundleArgs) string { return filepath.Join(a.configDir, "bundles.json") }

func bundlesList(args []string, stdout, stderr io.Writer) int {
	a, err := parseBundleArgs(args)
	if err != nil || len(a.pos) != 0 {
		if err != nil {
			bundlesPrintf(stderr, "bundles list: %v\n", err)
		}
		bundlesUsage(stderr)
		return 2
	}
	bundles, err := skills.LoadBundles(bundleFile(a))
	if err != nil {
		bundlesPrintf(stderr, "bundles list: %v\n", err)
		return 1
	}
	names := make([]string, 0, len(bundles))
	for name := range bundles {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		bundlesPrintf(stdout, "%s: %s\n", name, strings.Join(bundles[name].Skills, ", "))
	}
	return 0
}

func bundlesShow(args []string, stdout, stderr io.Writer) int {
	a, err := parseBundleArgs(args)
	if err != nil || len(a.pos) != 1 {
		if err != nil {
			bundlesPrintf(stderr, "bundles show: %v\n", err)
		}
		bundlesUsage(stderr)
		return 2
	}
	bundles, err := skills.LoadBundles(bundleFile(a))
	if err != nil {
		bundlesPrintf(stderr, "bundles show: %v\n", err)
		return 1
	}
	b, ok := bundles[a.pos[0]]
	if !ok {
		bundlesPrintf(stderr, "bundles show: %s not found\n", bundleArg(a.pos[0]))
		return 1
	}
	bundlesPrintf(stdout, "%s: %s\n", b.Name, strings.Join(b.Skills, ", "))
	return 0
}

func bundlesCreate(args []string, stdout, stderr io.Writer) int {
	a, err := parseBundleArgs(args)
	if err != nil || len(a.pos) < 2 {
		if err != nil {
			bundlesPrintf(stderr, "bundles create: %v\n", err)
		}
		bundlesUsage(stderr)
		return 2
	}
	ctx := context.Background()
	known, kerr := bundleKnown(ctx, a)
	if kerr != nil {
		bundlesPrintf(stderr, "bundles create: %v\n", kerr)
		return 1
	}
	b := skills.Bundle{Name: a.pos[0], Skills: append([]string(nil), a.pos[1:]...)}
	if verr := skills.ValidateBundle(b, func(n string) bool { return known[n] }); verr != nil {
		bundlesPrintf(stderr, "bundles create: %v\n", verr)
		return 2
	}
	bundles, err := skills.LoadBundles(bundleFile(a))
	if err != nil {
		bundlesPrintf(stderr, "bundles create: %v\n", err)
		return 1
	}
	if _, exists := bundles[b.Name]; exists {
		bundlesPrintf(stderr, "bundles create: %s already exists (delete it first)\n", b.Name)
		return 2
	}
	if len(bundles) >= skills.MaxBundles {
		bundlesPrintf(stderr, "bundles create: at most %d bundles\n", skills.MaxBundles)
		return 2
	}
	bundles[b.Name] = b
	if serr := skills.SaveBundles(bundleFile(a), bundles); serr != nil {
		bundlesPrintf(stderr, "bundles create: %v\n", serr)
		return 1
	}
	bundlesPrintf(stdout, "created %s: %s\n", b.Name, strings.Join(b.Skills, ", "))
	return 0
}

func bundlesDelete(args []string, stdout, stderr io.Writer) int {
	a, err := parseBundleArgs(args)
	if err != nil || len(a.pos) != 1 {
		if err != nil {
			bundlesPrintf(stderr, "bundles delete: %v\n", err)
		}
		bundlesUsage(stderr)
		return 2
	}
	if !a.yes {
		bundlesPrintf(stderr, "bundles delete: refusing to delete %s without --yes\n", bundleArg(a.pos[0]))
		return 2
	}
	bundles, err := skills.LoadBundles(bundleFile(a))
	if err != nil {
		bundlesPrintf(stderr, "bundles delete: %v\n", err)
		return 1
	}
	if _, ok := bundles[a.pos[0]]; !ok {
		bundlesPrintf(stderr, "bundles delete: %s not found\n", bundleArg(a.pos[0]))
		return 1
	}
	delete(bundles, a.pos[0])
	if serr := skills.SaveBundles(bundleFile(a), bundles); serr != nil {
		bundlesPrintf(stderr, "bundles delete: %v\n", serr)
		return 1
	}
	bundlesPrintf(stdout, "deleted %s\n", bundleArg(a.pos[0]))
	return 0
}

func bundlesReload(args []string, stdout, stderr io.Writer) int {
	a, err := parseBundleArgs(args)
	if err != nil || len(a.pos) != 0 {
		if err != nil {
			bundlesPrintf(stderr, "bundles reload: %v\n", err)
		}
		bundlesUsage(stderr)
		return 2
	}
	bundles, err := skills.LoadBundles(bundleFile(a))
	if err != nil {
		bundlesPrintf(stderr, "bundles reload: %v\n", err)
		return 1
	}
	bundlesPrintf(stdout, "loaded %d bundles from %s\n", len(bundles), bundleFile(a))
	return 0
}
