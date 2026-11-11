package main

import (
	"flag"
	"io"
	"path/filepath"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/setup"
)

// runSetupUninstall implements `damnbot setup --uninstall`.
func runSetupUninstall(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("setup --uninstall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dry := fs.Bool("dry-run", false, "print a unified diff without changing anything")
	purge := fs.Bool("purge-data", false, "remove the reported data/config directories")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		say(stderr, "setup --uninstall: unexpected argument %q\n", cleanArg(fs.Arg(0)))
		return 2
	}
	loader, err := config.New(config.OS())
	if err != nil {
		return setupFail(stderr, err)
	}
	paths, err := loader.Paths("")
	if err != nil {
		return setupFail(stderr, err)
	}
	home, err := config.OS().HomeDir()
	if err != nil {
		home = ""
	}
	report, err := setup.Uninstall(&setup.Options{
		ManifestPath: filepath.Join(paths.DataDir, setup.ManifestName),
		DryRun:       *dry,
		PurgeData:    *purge,
		DataDir:      paths.DataDir,
		ConfigDir:    paths.ConfigDir,
		HomeDir:      home,
		Out:          stdout,
	})
	if err != nil {
		return setupFail(stderr, err)
	}
	say(stdout, "uninstall: %d removed, %d kept\n", len(report.Removed), len(report.Kept))
	return 0
}
