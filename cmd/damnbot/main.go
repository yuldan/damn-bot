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
)

// version diisi lewat -ldflags "-X main.version=..." saat rilis.
var version = "dev"

func main() {
	utf8Console()
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
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

// run memisahkan logika dari os.Exit agar dapat diuji.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "run":
			return runHeadless(args[1:], stdout, stderr)
		case "undo":
			return runUndo(args[1:], stdout, stderr)
		}
	}
	if len(args) > 0 && args[0] == "sessions" {
		return runSessions(args[1:], stdout, stderr)
	}
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
	fs.Usage()
	return 2
}
