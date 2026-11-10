//go:build !minimal

package main

import (
	"flag"
	"io"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/connectors"
)

func runProxy(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("proxy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config path")
	if err := fs.Parse(args); err != nil || fs.NArg() == 0 {
		return f8ExitUsage
	}
	rest := fs.Args()
	switch rest[0] {
	case "start":
		if len(rest) != 1 {
			return f8ExitUsage
		}
		return f8CmdFail(stderr, "proxy", f8Unavail("proxy", "no OAuth provider is configured for the local proxy"))
	case "status":
		if len(rest) != 1 {
			return f8ExitUsage
		}
		say(stdout, "proxy stopped\n")
		return f8ExitOK
	case "providers":
		cfg, _, _, err := f8LoadSetup(*cfgPath, "")
		if err != nil {
			return f8CmdFail(stderr, "proxy", err)
		}
		if len(cfg.Providers.Custom) == 0 {
			say(stdout, "providers none\n")
			return f8ExitOK
		}
		for i := range cfg.Providers.Custom {
			p := &cfg.Providers.Custom[i]
			say(stdout, "provider %s api=%s models=%d\n", cleanLine(p.Name), cleanLine(p.API), len(p.Models))
		}
		return f8ExitOK
	default:
		say(stderr, "proxy: unknown subcommand %q\n", cleanArg(rest[0]))
		return f8ExitUsage
	}
}

func runMemory(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config path")
	if err := fs.Parse(args); err != nil || fs.NArg() == 0 {
		return f8ExitUsage
	}
	rest := fs.Args()
	switch rest[0] {
	case "status":
		cfg, _, _, err := f8LoadSetup(*cfgPath, "")
		if err != nil {
			return f8CmdFail(stderr, "memory", err)
		}
		say(stdout, "provider none\nactive false\nembeddings %s\n", cleanLine(cfg.Memory.Embeddings))
		return f8ExitOK
	case "setup", "off", "reset":
		return f8CmdFail(stderr, "memory", f8Unavail("memory", "external memory provider is not available in this build"))
	default:
		return f8ExitUsage
	}
}

func runConnectors(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("connectors", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config path")
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
		connectorsAccounts(&cfg, stdout)
		return f8ExitOK
	case "connect", "rename", "disconnect":
		if len(rest) < 2 {
			return f8ExitUsage
		}
		return f8CmdFail(stderr, "connectors", f8Unavail("connectors", "hosted connector accounts are not available in this build"))
	default:
		return f8ExitUsage
	}
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
