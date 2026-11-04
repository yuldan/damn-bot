//go:build !minimal

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"runtime"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/doctor"
)

type doctorToolsArgs struct {
	sub        string
	name       string
	positional []string
	jsonOut    bool
	yes        bool
}

// runDoctorTools implements `damnbot doctor tools ls|fetch|verify` (SPEC §10,
// GAP-TOOL-06). `ls` and `verify` read the data dir and the embedded lock;
// only `fetch` touches the network.
func runDoctorTools(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		doctorToolsUsage(stderr)
		return 2
	}
	parsed, ok := parseDoctorTools(args, stderr)
	if !ok {
		return 2
	}
	switch parsed.sub {
	case "ls":
		return doctorToolsList(parsed, stdout, stderr)
	case "verify":
		return doctorToolsVerify(parsed, stdout, stderr)
	case "fetch":
		return runDoctorToolsFetch(parsed, stdout, stderr)
	default:
		say(stderr, "doctor tools: unknown subcommand %q\n", cleanArg(parsed.sub))
		doctorToolsUsage(stderr)
		return 2
	}
}

func parseDoctorTools(args []string, stderr io.Writer) (doctorToolsArgs, bool) {
	parsed := doctorToolsArgs{sub: args[0]}
	flags := flag.NewFlagSet("doctor tools "+parsed.sub, flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOut := flags.Bool("json", false, "machine-readable status")
	yes := flags.Bool("yes", false, "confirm the displayed license for fetch")
	// Flags may follow the subcommand (Go's flag package stops at the first
	// positional), so split them out before parsing.
	flagsOnly := make([]string, 0, len(args))
	for _, a := range args[1:] {
		if strings.HasPrefix(a, "-") {
			flagsOnly = append(flagsOnly, a)
			continue
		}
		parsed.positional = append(parsed.positional, a)
	}
	if err := flags.Parse(flagsOnly); err != nil {
		return parsed, false
	}
	parsed.jsonOut, parsed.yes = *jsonOut, *yes
	if len(parsed.positional) > 1 {
		doctorToolsUsage(stderr)
		return parsed, false
	}
	if len(parsed.positional) == 1 {
		parsed.name = parsed.positional[0]
	}
	return parsed, true
}

// doctorToolsLoad resolves the lock, data dir and host platform.
func doctorToolsLoad(stderr io.Writer) (*doctor.ToolsLock, string, string, string, bool) {
	lock, err := doctor.EmbeddedToolsLock()
	if err != nil {
		say(stderr, "doctor tools: %s\n", cleanLine(err.Error()))
		return nil, "", "", "", false
	}
	root, err := doctorDataDir()
	if err != nil {
		say(stderr, "doctor tools: %s\n", cleanLine(err.Error()))
		return nil, "", "", "", false
	}
	return lock, root, runtime.GOOS, runtime.GOARCH, true
}

func doctorToolsList(parsed doctorToolsArgs, stdout, stderr io.Writer) int {
	lock, root, goos, goarch, ok := doctorToolsLoad(stderr)
	if !ok {
		return 1
	}
	list, ok := filterTools(doctor.ToolsList(lock, root, goos, goarch), parsed.name, stderr)
	if !ok {
		return 1
	}
	return printToolStatuses(list, parsed.jsonOut, stdout, stderr)
}

func doctorToolsVerify(parsed doctorToolsArgs, stdout, stderr io.Writer) int {
	lock, root, goos, goarch, ok := doctorToolsLoad(stderr)
	if !ok {
		return 1
	}
	list, verr := doctor.ToolsVerify(lock, root, goos, goarch)
	if parsed.name != "" {
		list, _ = filterTools(list, parsed.name, stderr)
	}
	if code := printToolStatuses(list, parsed.jsonOut, stdout, stderr); code != 0 {
		return code
	}
	if verr != nil {
		say(stderr, "doctor tools verify: %s\n", cleanLine(verr.Error()))
		return 1
	}
	return 0
}

// filterTools narrows the rows to name. An empty name keeps every row.
func filterTools(list []doctor.ToolStatus, name string, stderr io.Writer) ([]doctor.ToolStatus, bool) {
	if name == "" {
		return list, true
	}
	kept := make([]doctor.ToolStatus, 0, 1)
	for _, st := range list {
		if st.Name == name {
			kept = append(kept, st)
		}
	}
	if len(kept) == 0 {
		say(stderr, "doctor tools: %q is not in the lock\n", cleanArg(name))
		return nil, false
	}
	return kept, true
}

func printToolStatuses(list []doctor.ToolStatus, asJSON bool, stdout, stderr io.Writer) int {
	if asJSON {
		b, err := json.MarshalIndent(list, "", "  ")
		if err != nil {
			say(stderr, "doctor tools: json: %s\n", cleanLine(err.Error()))
			return 1
		}
		if code := say(stdout, "%s\n", b); code != 0 {
			return code
		}
		return 0
	}
	for _, st := range list {
		state := "ok"
		if !st.OK {
			state = "FAIL"
		}
		if code := say(stdout, "  %-4s %-24s %-10s %-26s %s\n", state, st.Name, st.Version, st.License, cleanLine(st.Detail)); code != 0 {
			return code
		}
	}
	return 0
}

func doctorToolsUsage(w io.Writer) {
	say(w, "usage: damnbot doctor tools <ls|fetch|verify> [<name>] [--json] [--yes]\n")
}

// doctorDataDir resolves the same data dir the rest of doctor uses.
func doctorDataDir() (string, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return "", fmt.Errorf("doctor tools: %w", err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return "", fmt.Errorf("doctor tools: %w", err)
	}
	return paths.DataDir, nil
}
