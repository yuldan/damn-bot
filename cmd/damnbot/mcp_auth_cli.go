//go:build !minimal

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
)

// runMCPImpl dispatches the subcommands of this slice.
func runMCPImpl(args []string, stdout, stderr io.Writer) int {
	switch args[0] {
	case "add":
		return mcpAdd(args[1:], stdout, stderr)
	case "list":
		return mcpList(args[1:], stdout, stderr)
	case "remove":
		return mcpRemove(args[1:], stdout, stderr)
	case "auth":
		return mcpAuth(args[1:], stdout, stderr)
	case "refresh":
		return mcpRefresh(args[1:], stdout, stderr)
	case "review":
		return mcpReview(args[1:], stdout, stderr)
	case "catalog":
		return mcpCatalog(args[1:], stdout, stderr)
	}
	sessionsPrintf(stderr, "mcp: unknown subcommand %q\n", cleanArg(args[0]))
	mcpUsage(stderr)
	return 2
}

func openMCPConfig() (*config.Loader, config.Config, config.Paths, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return nil, config.Config{}, config.Paths{}, fmt.Errorf("config deps: %w", err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return nil, config.Config{}, config.Paths{}, fmt.Errorf("config paths: %w", err)
	}
	cfg, err := loader.Load(paths.ConfigFile())
	if errors.Is(err, os.ErrNotExist) {
		cfg, err = loader.Parse(nil, paths.ConfigFile())
	}
	if err != nil {
		return nil, config.Config{}, config.Paths{}, fmt.Errorf("load config: %w", err)
	}
	return loader, cfg, paths, nil
}

// sliceFlag collects a repeatable string flag.
type sliceFlag []string

func (s *sliceFlag) String() string { return strings.Join(*s, ",") }

func (s *sliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func mcpAuth(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return mcpUsageErr(stderr, "auth login|logout|status expected")
	}
	switch args[0] {
	case "login":
		return mcpAuthLogin(args[1:], stdout, stderr)
	case "logout":
		return mcpAuthLogout(args[1:], stdout, stderr)
	case "status":
		return mcpAuthStatus(args[1:], stdout, stderr)
	}
	return mcpUsageErr(stderr, "auth: unknown subcommand %q", cleanArg(args[0]))
}

func mcpAuthLogin(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp auth login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	offline := fs.Bool("offline", false, "refuse network access")
	pos, perr := parseInterleaved(fs, args)
	if perr != nil {
		return 2
	}
	name, code := mcpNameArg(pos, "auth login <name>", stderr)
	if code != 0 {
		return code
	}
	tokens, err := mcpTokensFor(name, *offline, stdout)
	if err != nil {
		return mcpFail(stderr, "auth login", err)
	}
	tok, err := tokens.Login(context.Background())
	if err != nil {
		return mcpFail(stderr, "auth login", err)
	}
	sessionsPrintf(stdout, "login %s: ok (%s)\n", name, expiryLabel(&tok, tokens.now()))
	return 0
}

func mcpAuthLogout(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp auth logout", flag.ContinueOnError)
	fs.SetOutput(stderr)
	offline := fs.Bool("offline", false, "refuse network access")
	pos, perr := parseInterleaved(fs, args)
	if perr != nil {
		return 2
	}
	name, code := mcpNameArg(pos, "auth logout <name>", stderr)
	if code != 0 {
		return code
	}
	tokens, err := mcpTokensFor(name, *offline, io.Discard)
	if err != nil {
		return mcpFail(stderr, "auth logout", err)
	}
	state, err := tokens.Logout(context.Background())
	if err != nil {
		return mcpFail(stderr, "auth logout", err)
	}
	sessionsPrintf(stdout, "%s: %s\n", name, state)
	return 0
}

func mcpAuthStatus(args []string, stdout, stderr io.Writer) int { //nolint:gocognit // one report loop over servers and two output modes
	fs := flag.NewFlagSet("mcp auth status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "machine-readable status")
	offline := fs.Bool("offline", false, "report without refreshing")
	pos, perr := parseInterleaved(fs, args)
	if perr != nil {
		return 2
	}
	if len(pos) > 1 {
		return mcpUsageErr(stderr, "auth status takes at most one server name")
	}
	_, cfg, _, err := openMCPConfig()
	if err != nil {
		return mcpFail(stderr, "auth status", err)
	}
	servers := namedMCPServers(&cfg, pos)
	if len(servers) == 0 {
		return mcpFail(stderr, "auth status", errors.New("no server selected"))
	}
	rows := make([]mcpTokenStatus, 0, len(servers))
	failed := false
	for i := range servers {
		srv := &servers[i]
		if srv.OAuth == nil {
			if len(servers) == 1 {
				return mcpFail(stderr, "auth status", fmt.Errorf("server %s has no oauth section", srv.Name))
			}
			continue
		}
		tokens, terr := mcpTokensFor(srv.Name, *offline, io.Discard)
		if terr != nil {
			sessionsPrintf(stderr, "mcp auth status %s: %s\n", srv.Name, cleanLine(terr.Error()))
			failed = true
			continue
		}
		status := tokens.Status(context.Background())
		rows = append(rows, status)
		if status.State != "ok" {
			failed = true
		}
	}
	if *asJSON {
		raw, err := json.Marshal(rows)
		if err != nil {
			return mcpFail(stderr, "auth status", err)
		}
		if code := say(stdout, "%s\n", raw); code != 0 {
			return code
		}
	} else {
		for i := range rows {
			sessionsPrintf(stdout, "%s: %s%s\n", rows[i].Server, rows[i].State, tokenSuffix(&rows[i]))
		}
	}
	if failed {
		return 1
	}
	return 0
}

func tokenSuffix(s *mcpTokenStatus) string {
	if s.Expiry != "" && s.State == "ok" {
		return " (" + s.Expiry + ")"
	}
	if s.Error != "" {
		return ": " + s.Error
	}
	return ""
}

// mcpTokensFor opens the OAuth adapter of one configured server.
func mcpTokensFor(name string, offline bool, out io.Writer) (*mcpTokens, error) {
	_, cfg, _, err := openMCPConfig()
	if err != nil {
		return nil, err
	}
	srv, ok := mcpServerByName(&cfg, name)
	if !ok {
		return nil, fmt.Errorf("server %s is not configured", name)
	}
	if srv.OAuth == nil {
		return nil, fmt.Errorf("server %s has no oauth section", name)
	}
	ref := ""
	if section := cfg.Modules.MCP.Servers[name]; section.OAuth != nil {
		ref = section.OAuth.ClientSecret
	}
	return openMCPTokens(context.Background(), srv, ref, offline, out)
}

// openCLI's default data dir is the profile path; close ignores a nil store.
