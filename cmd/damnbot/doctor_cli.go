package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"io/fs"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/doctor"
)

// runDoctor implements the F3 read-only core of `damnbot doctor`
// (SELF-HEALING §8, GAP-OPS-06). `--verify`/`--network` and the doctor
// subcommands arrive with their engines and are not accepted as no-ops.
func runDoctor(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOut := flags.Bool("json", false, "machine-readable "+doctor.SchemaVersion)
	fix := flags.Bool("fix", false, "fix permission bits")
	flags.Bool("yes", false, "non-interactive --fix")
	flags.Bool("quick", false, "quick checks (all F3 checks are quick)")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		say(stderr, "doctor: subcommand %q is not available in F3\n", cleanArg(flags.Arg(0)))
		return 2
	}
	loader, err := config.New(config.OS())
	if err != nil {
		say(stderr, "doctor: %s\n", cleanLine(err.Error()))
		return 1
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		say(stderr, "doctor: %s\n", cleanLine(err.Error()))
		return 1
	}
	cfg, cfgErr := loader.Load(paths.ConfigFile())
	found := true
	if errors.Is(cfgErr, fs.ErrNotExist) {
		found, cfgErr, cfg = false, nil, config.Default()
	} else if cfgErr != nil {
		cfg = config.Default()
	}
	rep := doctor.Run(context.Background(), &doctor.Options{
		ConfigDir: paths.ConfigDir, DataDir: paths.DataDir, ConfigFile: paths.ConfigFile(),
		ConfigFound: found, ConfigErr: cfgErr, ProviderModel: cfg.Provider.Model, Fix: *fix,
	})
	if *jsonOut {
		b, merr := json.MarshalIndent(rep, "", "  ")
		if merr != nil {
			say(stderr, "doctor: json: %s\n", cleanLine(merr.Error()))
			return 1
		}
		if code := say(stdout, "%s\n", b); code != 0 {
			return code
		}
		return rep.Exit
	}
	say(stdout, "damnbot %s · data_dir %s · mode normal\n", version, paths.DataDir)
	for _, c := range rep.Checks {
		if code := say(stdout, "  %-6s %-8s %-12s %s\n", c.Status, c.ID, c.Name, cleanLine(c.Message)); code != 0 {
			return code
		}
	}
	say(stdout, "\n%d ok, %d warning, %d error, %d fixed. Detail: damnbot doctor --json\n",
		rep.Summary.OK, rep.Summary.Warn, rep.Summary.Error, rep.Summary.Fixed)
	return rep.Exit
}

func doctorUsage(w io.Writer) {
	say(w, "usage: damnbot doctor [--json] [--fix [--yes]] [--quick]\n")
}
