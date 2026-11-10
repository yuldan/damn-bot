//go:build !minimal

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"io/fs"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/doctor"
)

type doctorFlags struct {
	jsonOut, fix, network bool
}

func runDoctor(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "tools":
			return runDoctorTools(args[1:], stdout, stderr)
		case "bundle":
			return runDoctorBundle(args[1:], stdout, stderr)
		}
	}
	opt, ok := parseDoctorFlags(args, stderr)
	if !ok {
		return 2
	}
	loaded, ok := doctorState(stderr)
	if !ok {
		return 1
	}
	if opt.network {
		return doctorNetwork(&loaded.cfg, loaded.cfgErr, opt.jsonOut, stdout, stderr)
	}
	return doctorReport(&loaded, opt, stdout, stderr)
}

func parseDoctorFlags(args []string, stderr io.Writer) (doctorFlags, bool) {
	var opt doctorFlags
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOut := flags.Bool("json", false, "machine-readable "+doctor.SchemaVersion)
	fix := flags.Bool("fix", false, "fix permission bits")
	flags.Bool("yes", false, "non-interactive --fix")
	flags.Bool("quick", false, "quick checks (all F3 checks are quick)")
	network := flags.Bool("network", false, "list every outbound egress point and its status")
	if err := flags.Parse(args); err != nil {
		return opt, false
	}
	if flags.NArg() != 0 {
		say(stderr, "doctor: subcommand %q is not available in F3\n", cleanArg(flags.Arg(0)))
		return opt, false
	}
	return doctorFlags{jsonOut: *jsonOut, fix: *fix, network: *network}, true
}

// doctorLoaded is the config state one doctor run needs.
type doctorLoaded struct {
	paths  config.Paths
	cfg    config.Config
	cfgErr error
	found  bool
}

// doctorState loads the effective config and reports whether it was found.
func doctorState(stderr io.Writer) (doctorLoaded, bool) {
	loader, err := config.New(config.OS())
	if err != nil {
		say(stderr, "doctor: %s\n", cleanLine(err.Error()))
		return doctorLoaded{}, false
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		say(stderr, "doctor: %s\n", cleanLine(err.Error()))
		return doctorLoaded{}, false
	}
	cfg, cfgErr := loader.Load(paths.ConfigFile())
	found := true
	if errors.Is(cfgErr, fs.ErrNotExist) {
		found, cfgErr, cfg = false, nil, config.Default()
	} else if cfgErr != nil {
		cfg = config.Default()
	}
	if cfgErr == nil {
		cfgErr = loader.Effective(&cfg)
	}
	return doctorLoaded{paths: paths, cfg: cfg, cfgErr: cfgErr, found: found}, true
}

func doctorNetwork(cfg *config.Config, cfgErr error, asJSON bool, stdout, stderr io.Writer) int {
	if cfgErr != nil {
		say(stderr, "doctor --network: %s\n", cleanLine(cfgErr.Error()))
		return 1
	}
	return sayEgress(cfg, asJSON, stdout)
}

func doctorReport(loaded *doctorLoaded, opt doctorFlags, stdout, stderr io.Writer) int {
	rep := doctor.Run(context.Background(), &doctor.Options{
		ConfigDir: loaded.paths.ConfigDir, DataDir: loaded.paths.DataDir, ConfigFile: loaded.paths.ConfigFile(),
		ConfigFound: loaded.found, ConfigErr: loaded.cfgErr, ProviderModel: loaded.cfg.Provider.Model,
		ProviderWarnings: doctorProviderFindings(&loaded.cfg), Fix: opt.fix,
	})
	attachCredentialPoolChecks(&rep, &loaded.cfg)
	if opt.jsonOut {
		return printDoctorJSON(&rep, stdout, stderr)
	}
	say(stdout, "damnbot %s · data_dir %s · mode normal\n", version, loaded.paths.DataDir)
	for _, c := range rep.Checks {
		if code := say(stdout, "  %-6s %-8s %-12s %s\n", c.Status, c.ID, c.Name, cleanLine(c.Message)); code != 0 {
			return code
		}
	}
	say(stdout, "\n%d ok, %d warning, %d error, %d fixed. Detail: damnbot doctor --json\n",
		rep.Summary.OK, rep.Summary.Warn, rep.Summary.Error, rep.Summary.Fixed)
	return rep.Exit
}

func printDoctorJSON(rep *doctor.Report, stdout, stderr io.Writer) int {
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		say(stderr, "doctor: json: %s\n", cleanLine(err.Error()))
		return 1
	}
	if code := say(stdout, "%s\n", b); code != 0 {
		return code
	}
	return rep.Exit
}

// attachCredentialPoolChecks appends DOC-012 and re-derives the Run exit
// contract. It reads the named-secret index, never a value.
func attachCredentialPoolChecks(rep *doctor.Report, cfg *config.Config) {
	present := map[string]bool{}
	if st, err := secretsStore(); err == nil {
		if entries, lerr := st.List(); lerr == nil {
			for _, e := range entries {
				present[e.Name] = true
			}
		}
	}
	var out []doctor.CredentialPoolStatus
	for i := range cfg.Providers.Custom {
		c := &cfg.Providers.Custom[i]
		if c.Credentials == nil {
			continue
		}
		ids := make([]string, 0, len(c.Credentials))
		var missing []string
		for _, cred := range c.Credentials {
			ids = append(ids, cred.ID)
			if !present[strings.TrimPrefix(cred.Secret, "keychain:")] {
				missing = append(missing, cred.ID)
			}
		}
		out = append(out, doctor.CredentialPoolStatus{Provider: c.Name, IDs: ids, Missing: missing})
	}
	checks := doctor.PoolChecks(out)
	rep.Checks = append(rep.Checks, checks...)
	for _, c := range checks {
		switch c.Status {
		case doctor.StatusOK, doctor.StatusFixed:
		case doctor.StatusWarn:
			rep.Summary.Warn++
		case doctor.StatusError:
			rep.Summary.Error++
		}
	}
	switch {
	case rep.Summary.Error > 0:
		rep.Exit = 1
	case rep.Summary.Warn > 0:
		rep.Exit = 2
	case rep.Summary.Fixed > 0:
		rep.Exit = 3
	default:
		rep.Exit = 0
	}
}
