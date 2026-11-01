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
	"strconv"
	"strings"

	"github.com/damnbot/damnbot/internal/mcp"
)

// mcpAdd writes one validated [modules.mcp.servers.<name>] section atomically.
func mcpAdd(args []string, stdout, stderr io.Writer) int { //nolint:gocognit,gocyclo // flat flag validation is one sequence
	fs := flag.NewFlagSet("mcp add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	url := fs.String("url", "", "Streamable HTTP endpoint")
	command := fs.String("command", "", "stdio executable (exclusive with --url)")
	issuer := fs.String("issuer", "", "OAuth issuer (HTTPS or loopback)")
	clientID := fs.String("client-id", "", "OAuth client id; empty uses dynamic registration")
	clientSecret := fs.String("client-secret", "", "keychain:<id> reference to a client secret")
	trust := fs.String("trust", "untrusted", "untrusted or full")
	timeout := fs.Int("timeout", 120, "per-call timeout in seconds (1..600)")
	var argv, scopes, readOnly sliceFlag
	fs.Var(&argv, "arg", "stdio argument (repeatable)")
	fs.Var(&scopes, "scope", "OAuth scope (repeatable; comma-separated accepted)")
	fs.Var(&readOnly, "read-only", "tool name to mark read-only (repeatable)")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return 2
	}
	if len(pos) != 1 {
		return mcpUsageErr(stderr, "add needs exactly one server name")
	}
	name := pos[0]
	if !validMCPName(name) {
		return mcpUsageErr(stderr, "server name must match ^[a-z0-9][a-z0-9_-]{0,47}$")
	}
	if (*url == "") == (*command == "") {
		return mcpUsageErr(stderr, "add needs exactly one of --url or --command")
	}
	if *trust != "untrusted" && *trust != "full" {
		return mcpUsageErr(stderr, "--trust must be untrusted or full")
	}
	if *timeout < 1 || *timeout > 600 {
		return mcpUsageErr(stderr, "--timeout must be between 1 and 600")
	}
	if *issuer != "" && *command != "" {
		return mcpUsageErr(stderr, "--issuer is only valid with --url")
	}
	if *clientSecret != "" && !strings.HasPrefix(*clientSecret, "keychain:") {
		return mcpUsageErr(stderr, "--client-secret must be a keychain:<id> reference")
	}
	var flatScopes []string
	for _, s := range scopes {
		for part := range strings.SplitSeq(s, ",") {
			if part = strings.TrimSpace(part); part != "" {
				flatScopes = append(flatScopes, part)
			}
		}
	}
	loader, _, paths, err := openMCPConfig()
	if err != nil {
		return mcpFail(stderr, "add", err)
	}
	body, err := mcpAddBody(name, *url, *command, argv, *issuer, *clientID, *clientSecret, flatScopes, *trust, *timeout, readOnly)
	if err != nil {
		return mcpUsageErr(stderr, "%s", cleanLine(err.Error()))
	}
	path := paths.ConfigFile()
	merged, err := appendTOMPSection(path, name, body)
	if err != nil {
		return mcpFail(stderr, "add", err)
	}
	if _, err := loader.Parse(merged, path); err != nil {
		return mcpFail(stderr, "add", fmt.Errorf("validate config: %w", err))
	}
	if err := os.MkdirAll(paths.ConfigDir, 0o700); err != nil {
		return mcpFail(stderr, "add", err)
	}
	if err := writeAtomicFile(path, ".config.toml.mcp-*", merged); err != nil {
		return mcpFail(stderr, "add", err)
	}
	sessionsPrintf(stdout, "added mcp server %s to %s\n", name, path)
	return 0
}

// mcpAddBody renders one config section; every value is strconv.Quote'd.
func mcpAddBody(name, url, command string, argv []string, issuer, clientID, clientSecret string, scopes []string, trust string, timeout int, readOnly []string) (string, error) { //nolint:gocognit // one TOML renderer, branch per field
	var b strings.Builder
	b.WriteString("[modules.mcp.servers." + name + "]\n")
	if url != "" {
		b.WriteString("url = " + strconv.Quote(url) + "\n")
	} else {
		b.WriteString("command = [" + strconv.Quote(command) + "]\n")
		if len(argv) > 0 {
			parts := make([]string, len(argv))
			for i, a := range argv {
				parts[i] = strconv.Quote(a)
			}
			b.WriteString("args = [" + strings.Join(parts, ", ") + "]\n")
		}
	}
	if trust != "untrusted" {
		b.WriteString("trust = " + strconv.Quote(trust) + "\n")
	}
	if timeout != 120 {
		b.WriteString("timeout_s = " + strconv.Itoa(timeout) + "\n")
	}
	if issuer != "" {
		if len(scopes) == 0 {
			return "", errors.New("--issuer requires at least one --scope")
		}
		b.WriteString("\n[modules.mcp.servers." + name + ".oauth]\n")
		b.WriteString("issuer = " + strconv.Quote(issuer) + "\n")
		if clientID != "" {
			b.WriteString("client_id = " + strconv.Quote(clientID) + "\n")
		}
		if clientSecret != "" {
			b.WriteString("client_secret = " + strconv.Quote(clientSecret) + "\n")
		}
		parts := make([]string, len(scopes))
		for i, s := range scopes {
			parts[i] = strconv.Quote(s)
		}
		b.WriteString("scopes = [" + strings.Join(parts, ", ") + "]\n")
	}
	for _, tool := range readOnly {
		if tool == "" || len(tool) > 128 || strings.ContainsAny(tool, "\x00\r\n") {
			return "", fmt.Errorf("invalid --read-only tool %q", tool)
		}
		b.WriteString("\n[modules.mcp.servers." + name + ".tools." + strconv.Quote(tool) + "]\n")
		b.WriteString("read_only = true\n")
	}
	return b.String(), nil
}

// mcpList prints servers and credential state; it never prints a secret.
func mcpList(args []string, stdout, stderr io.Writer) int { //nolint:gocognit,gocyclo // one report loop and two output modes
	fs := flag.NewFlagSet("mcp list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return mcpUsageErr(stderr, "list takes no arguments")
	}
	_, cfg, _, err := openMCPConfig()
	if err != nil {
		return mcpFail(stderr, "list", err)
	}
	type row struct {
		Name      string          `json:"name"`
		Transport string          `json:"transport"`
		URL       string          `json:"url,omitempty"`
		Command   []string        `json:"command,omitempty"`
		Trust     string          `json:"trust"`
		TimeoutS  int             `json:"timeout_s"`
		OAuth     bool            `json:"oauth"`
		Issuer    string          `json:"issuer,omitempty"`
		Scope     string          `json:"scope,omitempty"`
		Auth      *mcpTokenStatus `json:"auth,omitempty"`
	}
	servers := cliMCPServers(&cfg)
	rows := make([]row, 0, len(servers))
	for i := range servers {
		srv := &servers[i]
		r := row{Name: srv.Name, Transport: "stdio", Command: srv.Command, Trust: string(srv.Trust), TimeoutS: srv.TimeoutS}
		if srv.URL != "" {
			r.Transport, r.URL = "http", srv.URL
		}
		if srv.OAuth != nil {
			r.OAuth, r.Issuer = true, srv.OAuth.Issuer
			ref := ""
			if section := cfg.Modules.MCP.Servers[srv.Name]; section.OAuth != nil {
				ref = section.OAuth.ClientSecret
			}
			if tokens, terr := openMCPTokens(context.Background(), *srv, ref, true, io.Discard); terr == nil {
				status := tokens.PeekStatus(context.Background())
				r.Auth, r.Scope = &status, status.Scope
			}
		}
		rows = append(rows, r)
	}
	if *asJSON {
		raw, err := json.Marshal(rows)
		if err != nil {
			return mcpFail(stderr, "list", err)
		}
		return say(stdout, "%s\n", raw)
	}
	if len(rows) == 0 {
		sessionsPrintf(stdout, "no mcp servers configured\n")
		return 0
	}
	for i := range rows {
		r := &rows[i]
		target := r.URL
		if target == "" {
			target = strings.Join(r.Command, " ")
		}
		auth := ""
		if r.OAuth {
			state := "missing"
			if r.Auth != nil {
				state = r.Auth.State
			}
			auth = " oauth=" + state
		}
		sessionsPrintf(stdout, "%s\t%s\t%s\ttrust=%s timeout=%ds%s\n",
			r.Name, r.Transport, cleanLine(target), r.Trust, r.TimeoutS, auth)
	}
	return 0
}

// mcpRemove revokes the credential, then deletes the section and its subtables.
func mcpRemove(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp remove", flag.ContinueOnError)
	fs.SetOutput(stderr)
	offline := fs.Bool("offline", false, "refuse network access (revoke is then skipped and removal fails)")
	pos, perr := parseInterleaved(fs, args)
	if perr != nil {
		return 2
	}
	name, code := mcpNameArg(pos, "remove <name>", stderr)
	if code != 0 {
		return code
	}
	loader, cfg, paths, err := openMCPConfig()
	if err != nil {
		return mcpFail(stderr, "remove", err)
	}
	srv, ok := mcpServerByName(&cfg, name)
	if !ok {
		return mcpFail(stderr, "remove", fmt.Errorf("server %s is not configured", name))
	}
	if srv.OAuth != nil {
		ref := ""
		if section := cfg.Modules.MCP.Servers[name]; section.OAuth != nil {
			ref = section.OAuth.ClientSecret
		}
		tokens, terr := openMCPTokens(context.Background(), srv, ref, *offline, io.Discard)
		if terr != nil {
			return mcpFail(stderr, "remove", terr)
		}
		state, rerr := tokens.Logout(context.Background())
		switch {
		case rerr != nil && errors.Is(rerr, mcp.ErrAuthRequired):
			// No stored token: nothing to revoke, removal continues.
		case rerr != nil:
			return mcpFail(stderr, "remove", fmt.Errorf("revoke before removal: %w", rerr))
		default:
			sessionsPrintf(stdout, "%s: %s\n", name, state)
		}
	}
	merged, err := removeTOMPSection(paths.ConfigFile(), name)
	if err != nil {
		return mcpFail(stderr, "remove", err)
	}
	if _, err := loader.Parse(merged, paths.ConfigFile()); err != nil {
		return mcpFail(stderr, "remove", fmt.Errorf("validate config: %w", err))
	}
	if err := writeAtomicFile(paths.ConfigFile(), ".config.toml.mcp-*", merged); err != nil {
		return mcpFail(stderr, "remove", err)
	}
	sessionsPrintf(stdout, "removed mcp server %s\n", name)
	return 0
}

// appendTOMPSection appends one server block after checking the name is new.
func appendTOMPSection(path, name, body string) ([]byte, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path comes from config.Paths
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if hasTOMPSection(data, name) {
		return nil, fmt.Errorf("server %s already exists in %s", name, path)
	}
	out := append([]byte{}, data...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	if len(out) > 0 {
		out = append(out, '\n')
	}
	out = append(out, body...)
	return out, nil
}

func sectionPrefix(name string) string { return "modules.mcp.servers." + name }

func mcpSection(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") {
		return "", false
	}
	end := strings.IndexByte(trimmed, ']')
	if end < 0 {
		return "", false
	}
	return strings.TrimSpace(trimmed[1:end]), true
}

func hasTOMPSection(data []byte, name string) bool {
	prefix := sectionPrefix(name)
	for line := range strings.SplitSeq(string(data), "\n") {
		if section, ok := mcpSection(line); ok && (section == prefix || strings.HasPrefix(section, prefix+".")) {
			return true
		}
	}
	return false
}

// removeTOMPSection deletes one block and its subtables, leaving the rest intact.
func removeTOMPSection(path, name string) ([]byte, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path comes from config.Paths
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	prefix := sectionPrefix(name)
	lines := strings.Split(string(data), "\n")
	out := make([]string, 0, len(lines))
	skip, found := false, false
	for _, line := range lines {
		if section, ok := mcpSection(line); ok {
			skip = section == prefix || strings.HasPrefix(section, prefix+".")
			found = found || skip
		}
		if !skip {
			out = append(out, line)
		}
	}
	if !found {
		return nil, fmt.Errorf("server %s is not in %s", name, path)
	}
	merged := strings.Join(out, "\n")
	merged = strings.TrimRight(merged, "\n") + "\n"
	return []byte(merged), nil
}
