// Command damnbot adalah composition root (ENGINEERING §2.1, E-08).
// F0: hanya kerangka; subcommand, config, dan wiring DI ditambahkan mulai F1.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/modules"
)

// version diisi lewat -ldflags "-X main.version=..." saat rilis.
var version = "dev"

func main() {
	utf8Console() // EC-WIN-017: the attached console speaks UTF-8 before anything prints
	os.Exit(start(modules.Freeze(), os.Args[1:], os.Stdout, os.Stderr))
}

// printEngine is the REL-04 verbose surface.
func printEngine(stdout io.Writer) int {
	e, err := ctxdb.ProbeEngine(context.Background())
	if err != nil {
		return 1
	}
	if _, err := fmt.Fprintf(stdout, "sqlite %s fts5=%t contentless_delete=%t trigram=%t json=%t supported=%t\n",
		e.Version, e.FTS5, e.ContentlessDelete, e.Trigram, e.JSON, e.Supported()); err != nil {
		return 1
	}
	return 0
}

// dispatchCommand routes the commands that own their flag sets; ok is false
// when args do not name a callable command, so the root command's flags run
// next. The surface registry is the single list (ADR-197): an entry without
// Run is documented but not implemented, and is never dispatched as a stub.
func dispatchCommand(args []string, stdout, stderr io.Writer) (code int, ok bool) {
	if e, found := lookupSurface(args[0]); found && e.Run != nil {
		return e.Run(args[1:], stdout, stderr), true
	}
	if len(args) > 1 && args[0] == "eval" && args[1] == "recall" {
		return runEvalRecall(args[2:], stdout, stderr), true
	}
	return 0, false
}

// run memisahkan logika dari os.Exit agar dapat diuji. Flag global SPEC §10
// diparse sebelum dispatch, lalu help/`--help`/`<cmd> --help` dilayani dari
// registry permukaan sebelum perintah melihat argumennya.
func run(args []string, stdout, stderr io.Writer) int {
	return runWith(args, stdout, stderr, runTUI)
}

// runWith is run with an injectable interactive surface, so tests exercise the
// dispatch matrix without starting a terminal program.
func runWith(args []string, stdout, stderr io.Writer, openTUI func([]string) int) int {
	g, rest, err := parseGlobalArgs(args)
	if err != nil {
		if _, werr := fmt.Fprintf(stderr, "damnbot: %s\n", cleanLine(err.Error())); werr != nil {
			return 2
		}
		return 2
	}
	setActiveProfile(g.profile)
	if code, ok := surfaceOrCommand(rest, stdout, stderr); ok {
		return code
	}
	return rootFlags(rest, stdout, stderr, openTUI)
}

// surfaceOrCommand melayani bentuk help dari registry lalu dispatch perintah;
// ok=false berarti flag root (dan TUI nanti) yang memiliki argumen ini.
func surfaceOrCommand(args []string, stdout, stderr io.Writer) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	switch args[0] {
	case "help":
		return surfaceHelp(stdout, stderr, args[1:]), true
	case "-h", "--help":
		return surfaceHelp(stdout, stderr, nil), true
	}
	if len(args) > 1 && (args[1] == "-h" || args[1] == "--help") {
		if _, found := lookupSurface(args[0]); found {
			return surfaceHelp(stdout, stderr, args[:1]), true
		}
	}
	return dispatchCommand(args, stdout, stderr)
}

// rootFlags parses the root flags (`-version`, `-verbose`). Bare `damnbot`
// opens the interactive TUI; any other unflagged argument keeps the usage
// screen so typos never start a full-screen program.
func rootFlags(args []string, stdout, stderr io.Writer, openTUI func([]string) int) int {
	fs := flag.NewFlagSet("damnbot", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "tampilkan versi lalu keluar")
	verbose := fs.Bool("verbose", false, "dengan -version: probe engine SQLite (REL-04)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		if _, err := fmt.Fprintln(stdout, "damnbot", version); err != nil {
			return 1
		}
		if *verbose {
			return printEngine(stdout)
		}
		return 0
	}
	if len(args) == 0 {
		return openTUI(args)
	}
	fs.Usage()
	return 2
}
