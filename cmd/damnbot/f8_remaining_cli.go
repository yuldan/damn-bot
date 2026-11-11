//go:build !minimal

package main

import (
	"flag"
	"fmt"
	"io"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/connectors"
)

func runConnectors(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("connectors", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config path")
	dataDir := fs.String("data-dir", "", "data directory")
	if err := fs.Parse(args); err != nil || fs.NArg() == 0 {
		return f8ExitUsage
	}
	rest := fs.Args()
	switch rest[0] {
	case "list":
		for _, d := range connectors.Builtin() {
			say(stdout, "%s scopes=%d tools=%d\n", d.Name, len(d.Scopes), len(d.Tools))
		}
		return f8ExitOK
	case "accounts":
		cfg, _, _, err := f8LoadSetup(*cfgPath, "")
		if err != nil {
			return f8CmdFail(stderr, "connectors", err)
		}
		store, err := connectorsStore(*dataDir)
		if err != nil {
			return f8CmdFail(stderr, "connectors", err)
		}
		connectorsHosted(store, stdout)
		connectorsAccounts(&cfg, stdout)
		return f8ExitOK
	case "connect", "rename", "disconnect":
		store, err := connectorsStore(*dataDir)
		if err != nil {
			return f8CmdFail(stderr, "connectors", err)
		}
		return connectorsMutate(rest, store, stdout, stderr)
	default:
		return f8ExitUsage
	}
}

// connectorsStore opens the same 0600 hosted-account store the module reads.
func connectorsStore(dataDir string) (*connectors.Connections, error) {
	dir, err := f8Dir(dataDir)
	if err != nil {
		return nil, err
	}
	store, err := connectors.OpenConnections(f8StorePath(dir, "hosted.json"))
	if err != nil {
		return nil, fmt.Errorf("hosted accounts: %w", err)
	}
	return store, nil
}

// connectorsHosted prints references only: a token value is never stored in
// the file and never reaches stdout.
func connectorsHosted(store *connectors.Connections, stdout io.Writer) {
	for _, acc := range store.List("") {
		say(stdout, "hosted %s/%s label=%s token=keychain\n", acc.Connector, acc.ID, cleanLine(acc.Label))
	}
}

func connectorsMutate(rest []string, store *connectors.Connections, stdout, stderr io.Writer) int {
	usage := func() int {
		say(stderr, "pemakaian: damnbot connectors connect <connector> <account> [label]\n")
		say(stderr, "           damnbot connectors rename <connector> <account> <label>\n")
		say(stderr, "           damnbot connectors disconnect <connector> <account>\n")
		return f8ExitUsage
	}
	switch rest[0] {
	case "connect":
		if len(rest) < 3 || len(rest) > 4 {
			return usage()
		}
		label := rest[2]
		if len(rest) == 4 {
			label = rest[3]
		}
		if _, err := store.Add(rest[1], rest[2], label, "keychain:connectors/"+rest[1]+"/"+rest[2]); err != nil {
			return f8CmdFail(stderr, "connectors", f8BadArg("%s", err))
		}
		say(stdout, "connected %s/%s token=keychain\n", rest[1], rest[2])
	case "rename":
		if len(rest) != 4 {
			return usage()
		}
		if err := store.Rename(rest[1], rest[2], rest[3]); err != nil {
			return f8CmdFail(stderr, "connectors", f8BadArg("%s", err))
		}
		say(stdout, "renamed %s/%s\n", rest[1], rest[2])
	case "disconnect":
		if len(rest) != 3 {
			return usage()
		}
		if err := store.Remove(rest[1], rest[2]); err != nil {
			return f8CmdFail(stderr, "connectors", f8BadArg("%s", err))
		}
		say(stdout, "disconnected %s/%s\n", rest[1], rest[2])
	}
	return f8ExitOK
}

func connectorsAccounts(cfg *config.Config, stdout io.Writer) {
	c := cfg.Modules.Connectors
	for _, r := range []struct {
		name, secret string
		on           bool
	}{
		{"graph", c.Graph.ClientSecretEnv, c.Graph.Enabled},
		{"teams", "", c.Teams.Enabled},
		{"feishu", c.Feishu.AppSecretEnv, c.Feishu.Enabled},
		{"meet", c.Meet.NodeTokenEnv, c.Meet.Enabled},
		{"xsearch", c.XSearch.KeyEnv, c.XSearch.Enabled},
		{"discord", c.Discord.BotTokenEnv, c.Discord.Enabled},
		{"osv", "", c.OSV.Enabled},
	} {
		if r.secret == "" {
			r.secret = "-"
		}
		say(stdout, "%s enabled=%t secret_env=%s\n", r.name, r.on, cleanLine(r.secret))
	}
}
