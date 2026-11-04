package main

import (
	"encoding/json"
	"io"
	"slices"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

// runDoctor implements the F3 read-only core of `damnbot doctor`
// (SELF-HEALING §8, GAP-OPS-06) and the egress listing of `--network`
// (GAP-OPS-02). `--verify` and the doctor subcommands arrive with their
// engines and are not accepted as no-ops.

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
	say(w, "       damnbot doctor tools <ls|verify> [<name>] [--json]\n")
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
