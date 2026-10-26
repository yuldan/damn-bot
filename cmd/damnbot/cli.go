package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
)

// cliEnv is what a non-turn CLI command (undo, review) works on: the profile
// config and the data dir's ctxdb opened as a `cli` holder. The caller
// closes db.
type cliEnv struct {
	cfg       config.Config
	configDir string
	db        *ctxdb.Store
}

func openCLI(ctx context.Context, configPath, dataDir string) (*cliEnv, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return nil, fmt.Errorf("config deps: %w", err)
	}
	cfg, err := loadRunConfig(loader, configPath)
	if err != nil {
		return nil, err
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return nil, fmt.Errorf("config paths: %w", err)
	}
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: cmp.Or(dataDir, paths.DataDir), Kind: "cli"})
	if err != nil {
		return nil, fmt.Errorf("open ctxdb: %w", err)
	}
	return &cliEnv{cfg: cfg, configDir: paths.ConfigDir, db: db}, nil
}

// runSub runs one non-turn subcommand: a parse failure exits 2 (printed once,
// the flag package may already have printed it), a command failure is
// printed as "<name>: <err>" (sanitized: errors carry journaled paths) and
// exits with the command's code.
func runSub[O any](name string, args []string, stdout, stderr io.Writer,
	parse func([]string, io.Writer) (O, error), cmd func(context.Context, *O, io.Writer) (int, error),
) int {
	o, err := parse(args, stderr)
	if err != nil {
		if _, reported := errors.AsType[*reportedParseError](err); !reported {
			if _, werr := fmt.Fprintf(stderr, "%s: %s\n", name, cleanLine(err.Error())); werr != nil {
				return 2
			}
		}
		return 2
	}
	code, err := cmd(context.Background(), &o, stdout)
	if err != nil {
		if _, werr := fmt.Fprintf(stderr, "%s: %s\n", name, cleanLine(err.Error())); werr != nil {
			return 2
		}
	}
	return code
}

// parseInterleaved parses fs over args, allowing flags after positional
// arguments (`undo 3 --dry-run`), and returns the positional arguments.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, &reportedParseError{err: fmt.Errorf("parse flags: %w", err)}
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos, args = append(pos, fs.Arg(0)), fs.Args()[1:]
	}
}

// unsafeRune reports runes that can drive or forge terminal output: control
// (Cc: ESC, CR/LF, C1), format (Cf: bidi overrides/isolates U+202A–202E,
// U+2066–2069, zero-width), and line/paragraph separators.
func unsafeRune(r rune) bool { return unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) }

// cleanLine renders untrusted text (paths, scopes, stored content, error
// text) as one terminal-safe line: unsafe runes and whitespace runs become
// one space. Invalid UTF-8 bytes (Linux names may hold any bytes) are
// replaced first, so a C1 byte such as 0x9B cannot reach an 8-bit
// terminal as a CSI (SEC5-13).
func cleanLine(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unsafeRune(r) }), " ")
}

// cleanBlock sanitizes multi-line text (a diff) line by line, keeping the
// line structure and indentation: tabs become spaces, other unsafe runes
// are dropped.
func cleanBlock(s string) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.Map(func(r rune) rune {
			switch {
			case r == '\t':
				return ' '
			case unsafeRune(r):
				return -1
			}
			return r
		}, l)
	}
	return strings.Join(lines, "\n") + "\n"
}
