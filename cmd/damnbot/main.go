// Command damnbot adalah composition root (ENGINEERING §2.1, E-08).
// F0: hanya kerangka; subcommand, config, dan wiring DI ditambahkan mulai F1.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
)

// version diisi lewat -ldflags "-X main.version=..." saat rilis.
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run memisahkan logika dari os.Exit agar dapat diuji.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "run" {
		return runHeadless(args[1:], stdout, stderr)
	}
	fs := flag.NewFlagSet("damnbot", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "tampilkan versi lalu keluar")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		if _, err := fmt.Fprintln(stdout, "damnbot", version); err != nil {
			return 1
		}
		return 0
	}
	fs.Usage()
	return 2
}
