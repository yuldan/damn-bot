package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"io/fs"
	"slices"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/doctor"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

// runDoctor implements the F3 read-only core of `damnbot doctor`
// (SELF-HEALING §8, GAP-OPS-06) and the egress listing of `--network`
// (GAP-OPS-02). `--verify` and the doctor subcommands arrive with their
// engines and are not accepted as no-ops.
func runDoctor(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOut := flags.Bool("json", false, "machine-readable "+doctor.SchemaVersion)
	fix := flags.Bool("fix", false, "fix permission bits")
	flags.Bool("yes", false, "non-interactive --fix")
	flags.Bool("quick", false, "quick checks (all F3 checks are quick)")
	network := flags.Bool("network", false, "list every outbound egress point and its status")
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
	if cfgErr == nil {
		cfgErr = loader.Effective(&cfg)
	}
	if *network {
		if cfgErr != nil {
			say(stderr, "doctor --network: %s\n", cleanLine(cfgErr.Error()))
			return 1
		}
		return sayEgress(&cfg, *jsonOut, stdout)
	}
	warnings := doctorProviderFindings(&cfg)
	opts := &doctor.Options{
		ConfigDir: paths.ConfigDir, DataDir: paths.DataDir, ConfigFile: paths.ConfigFile(),
		ConfigFound: found, ConfigErr: cfgErr, ProviderModel: cfg.Provider.Model,
		ProviderWarnings: warnings, Fix: *fix,
	}
	attachCredentialPools(opts, &cfg)
	rep := doctor.Run(context.Background(), opts)
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

// doctorProviderFindings loads the catalog and price table for DOC-009. A
// broken embedded table is itself a warning, not a reason to skip the rest of
// the read-only checks.
func doctorProviderFindings(cfg *config.Config) []string {
	catalog, err := registry.DefaultCatalog()
	if err != nil {
		return []string{"provider catalog: " + err.Error()}
	}
	table, err := registry.DefaultPriceTable()
	if err != nil {
		return []string{"pricing table: " + err.Error()}
	}
	return providerDoctorWarnings(cfg, catalog, table)
}

func doctorUsage(w io.Writer) {
	say(w, "usage: damnbot doctor [--json] [--fix [--yes]] [--quick] [--network]\n")
}

// networkRow is one egress registry line of `doctor --network`.
type networkRow struct {
	Name    string `json:"name"`
	Purpose string `json:"purpose"`
	Host    string `json:"host"`
	Trigger string `json:"trigger"`
	Status  string `json:"status"`
}

// sayEgress renders the static registry (netx.Registry) with the status the
// effective [network] config gives each purpose (GAP-OPS-02).
func sayEgress(cfg *config.Config, asJSON bool, stdout io.Writer) int {
	reg := netx.Registry()
	names := make([]string, 0, len(reg))
	for name := range reg {
		names = append(names, name)
	}
	slices.Sort(names)
	rows := make([]networkRow, 0, len(names))
	for _, name := range names {
		e := reg[name]
		rows = append(rows, networkRow{name, e.Purpose.String(), e.Host, e.Trigger, egressStatus(cfg, e.Purpose)})
	}
	if asJSON {
		b, err := json.MarshalIndent(struct {
			Offline    bool         `json:"offline"`
			Background bool         `json:"background"`
			Egress     []networkRow `json:"egress"`
		}{cfg.Network.Offline, cfg.Network.Background, rows}, "", "  ")
		if err != nil {
			return 1
		}
		return say(stdout, "%s\n", b)
	}
	if code := say(stdout, "egress registry · offline=%t · background=%t\n", cfg.Network.Offline, cfg.Network.Background); code != 0 {
		return code
	}
	for _, row := range rows {
		if code := say(stdout, "  %-10s %-10s %-22s %s\n              trigger: %s\n", row.Name, row.Purpose, row.Status, row.Host, row.Trigger); code != 0 {
			return code
		}
	}
	return 0
}

// egressStatus reports what the current policy does to one purpose: offline
// refuses every non-loopback dial, background=false refuses Background, and
// an inference endpoint may still be a local model under offline.
func egressStatus(cfg *config.Config, p netx.Purpose) string {
	switch {
	case cfg.Network.Offline && p != netx.Inference:
		return "disabled (offline_mode)"
	case p == netx.Background && !cfg.Network.Background:
		return "disabled (egress_disabled)"
	case cfg.Network.Offline:
		return "loopback only (offline_mode)"
	default:
		return "enabled"
	}
}
