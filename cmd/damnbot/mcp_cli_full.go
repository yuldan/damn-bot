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

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/mcpstore"
	"github.com/damnbot/damnbot/internal/mcp"
	"github.com/damnbot/damnbot/internal/netx"
)

// runMCPImpl dispatches the full-build MCP command set.
func runMCPImpl(args []string, stdout, stderr io.Writer) int {
	switch args[0] {
	case "add":
		return mcpAdd(args[1:], stdout, stderr)
	case "list":
		return mcpList(args[1:], stdout, stderr)
	case "remove":
		return mcpRemove(args[1:], stdout, stderr)
	case "refresh":
		return mcpRefresh(args[1:], stdout, stderr)
	case "review":
		return mcpReview(args[1:], stdout, stderr)
	case "auth":
		return mcpAuth(args[1:], stdout, stderr)
	}
	sessionsPrintf(stderr, "mcp: unknown subcommand %q\n", cleanArg(args[0]))
	mcpUsage(stderr)
	return 2
}

// openMCPConfig resolves the profile config without opening the database.
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
		return nil, config.Config{}, config.Paths{}, err
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

func mcpAdd(args []string, stdout, stderr io.Writer) int {
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
		for _, part := range strings.Split(s, ",") {
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
		return mcpFail(stderr, "add", err)
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

// mcpAddBody renders one strict config section. Values are quoted by
// strconv.Quote, so no operator input can break out of the TOML string.
func mcpAddBody(name, url, command string, argv []string, issuer, clientID, clientSecret string, scopes []string, trust string, timeout int, readOnly []string) (string, error) {
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

// mcpList prints configured servers and credential state. It never prints a
// token, a client secret, or a keychain value.
func mcpList(args []string, stdout, stderr io.Writer) int {
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
	rows := make([]row, 0, len(cfg.Modules.MCP.Servers))
	for _, srv := range cliMCPServers(&cfg) {
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
			if tokens, terr := openMCPTokens(context.Background(), srv, ref, true, io.Discard); terr == nil {
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
	for _, r := range rows {
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
		return mcpFail(stderr, "remove", err)
	}
	if err := writeAtomicFile(paths.ConfigFile(), ".config.toml.mcp-*", merged); err != nil {
		return mcpFail(stderr, "remove", err)
	}
	sessionsPrintf(stdout, "removed mcp server %s\n", name)
	return 0
}

func mcpRefresh(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp refresh", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pos, perr := parseInterleaved(fs, args)
	if perr != nil {
		return 2
	}
	if len(pos) > 1 {
		return mcpUsageErr(stderr, "refresh takes at most one server name")
	}
	env, err := openCLI(context.Background(), "", "")
	if err != nil {
		return mcpFail(stderr, "refresh", err)
	}
	defer mcpCloseDB(env.db, stderr)
	servers := namedMCPServers(&env.cfg, pos)
	if len(servers) == 0 {
		return mcpFail(stderr, "refresh", errors.New("no server selected"))
	}
	host := cliMCPHost(env.db, stdin(), stdout, stderr)
	failed := 0
	for _, srv := range servers {
		client, cerr := newMCPClient(host, srv, &env.cfg)
		if cerr != nil {
			sessionsPrintf(stderr, "mcp refresh %s: %s\n", srv.Name, cleanLine(cerr.Error()))
			failed++
			continue
		}
		tools, lerr := client.RefreshTools(context.Background())
		closeErr := client.Close()
		if lerr != nil {
			sessionsPrintf(stderr, "mcp refresh %s: %s\n", srv.Name, cleanLine(lerr.Error()))
			failed++
			continue
		}
		if closeErr != nil {
			sessionsPrintf(stderr, "mcp refresh %s: close: %s\n", srv.Name, cleanLine(closeErr.Error()))
		}
		sessionsPrintf(stdout, "%s: %d tools\n", srv.Name, len(tools))
	}
	if failed > 0 {
		return 1
	}
	return 0
}

func mcpReview(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp review", flag.ContinueOnError)
	fs.SetOutput(stderr)
	tool := fs.String("tool", "", "approve one changed tool instead of every changed tool")
	pos, perr := parseInterleaved(fs, args)
	if perr != nil {
		return 2
	}
	if len(pos) > 1 {
		return mcpUsageErr(stderr, "review takes at most one server name")
	}
	env, err := openCLI(context.Background(), "", "")
	if err != nil {
		return mcpFail(stderr, "review", err)
	}
	defer mcpCloseDB(env.db, stderr)
	servers := namedMCPServers(&env.cfg, pos)
	if len(servers) == 0 {
		return mcpFail(stderr, "review", errors.New("no server selected"))
	}
	host := cliMCPHost(env.db, stdin(), stdout, stderr)
	failed := 0
	for _, srv := range servers {
		client, cerr := newMCPClient(host, srv, &env.cfg)
		if cerr != nil {
			sessionsPrintf(stderr, "mcp review %s: %s\n", srv.Name, cleanLine(cerr.Error()))
			failed++
			continue
		}
		pins, perr := client.Pins(context.Background())
		if perr != nil {
			_ = client.Close() //nolint:errcheck // diagnostic path
			sessionsPrintf(stderr, "mcp review %s: %s\n", srv.Name, cleanLine(perr.Error()))
			failed++
			continue
		}
		for _, pin := range pins {
			if *tool != "" && pin.Tool != *tool {
				continue
			}
			switch pin.State {
			case mcp.PinPinned:
				sessionsPrintf(stdout, "%s/%s: pinned\n", srv.Name, pin.Tool)
			case mcp.PinQuarantined:
				// A removed definition has nothing to approve; a new listing
				// must pin it again before review can consider it.
				sessionsPrintf(stdout, "%s/%s: quarantined (removed definition; not approvable)\n", srv.Name, pin.Tool)
			default:
				sessionsPrintf(stdout, "%s/%s: changed; reviewing\n", srv.Name, pin.Tool)
				if rerr := client.Review(context.Background(), pin.Tool, "cli"); rerr != nil {
					sessionsPrintf(stderr, "mcp review %s/%s: %s\n", srv.Name, pin.Tool, cleanLine(rerr.Error()))
					failed++
				}
			}
		}
		if err := client.Close(); err != nil {
			sessionsPrintf(stderr, "mcp review %s: close: %s\n", srv.Name, cleanLine(err.Error()))
		}
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// namedMCPServers selects all servers or the one named.
func namedMCPServers(cfg *config.Config, names []string) []mcp.Server {
	if len(names) == 1 {
		if srv, ok := mcpServerByName(cfg, names[0]); ok {
			return []mcp.Server{srv}
		}
		return nil
	}
	return cliMCPServers(cfg)
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

func mcpAuthStatus(args []string, stdout, stderr io.Writer) int {
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
	for _, srv := range servers {
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
func mcpCloseDB(db *ctxdb.Store, stderr io.Writer) {
	if db == nil {
		return
	}
	if err := db.Close(); err != nil {
		sessionsPrintf(stderr, "mcp: close db: %s\n", cleanLine(err.Error()))
	}
}

// pinStoreFor adapts ctxdb/mcpstore to the module's PinStore seam (ADR-272).
func pinStoreFor(db *ctxdb.Store) (mcp.PinStore, error) {
	if db == nil {
		return nil, errors.New("mcp: no database for pins")
	}
	return cliPins{store: mcpstore.Store{DB: db}}, nil
}

type cliPins struct{ store mcpstore.Store }

func (p cliPins) Pin(ctx context.Context, server, tool string) (mcp.Pin, bool, error) {
	pin, ok, err := p.store.Pin(ctx, server, tool)
	if err != nil {
		return mcp.Pin{}, false, err
	}
	return toMCPPin(pin), ok, nil
}

func (p cliPins) Pins(ctx context.Context, server string) ([]mcp.Pin, error) {
	pins, err := p.store.Pins(ctx, server)
	if err != nil {
		return nil, err
	}
	out := make([]mcp.Pin, len(pins))
	for i, pin := range pins {
		out[i] = toMCPPin(pin)
	}
	return out, nil
}

func (p cliPins) PutPin(ctx context.Context, pin mcp.Pin) error {
	return p.store.PutPin(ctx, mcpstore.Pin{
		Server: pin.Server, Tool: pin.Tool, SchemaSHA256: pin.SchemaSHA256,
		FirstSeen: pin.FirstSeen, ApprovedAt: pin.ApprovedAt, ApprovedBy: pin.ApprovedBy,
		State: mcpstore.State(pin.State), Diff: pin.Diff,
	})
}

func (p cliPins) ApprovePin(ctx context.Context, server, tool, digest, by string, at int64) (bool, error) {
	return p.store.ApprovePin(ctx, server, tool, digest, by, at)
}

func toMCPPin(pin mcpstore.Pin) mcp.Pin {
	return mcp.Pin{
		Server: pin.Server, Tool: pin.Tool, SchemaSHA256: pin.SchemaSHA256,
		FirstSeen: pin.FirstSeen, ApprovedAt: pin.ApprovedAt, ApprovedBy: pin.ApprovedBy,
		State: mcp.PinState(pin.State), Diff: pin.Diff,
	}
}

// mcpDoer derives the MCP HTTP edge from the shared netx policy (E-05).
func mcpDoer() *netx.Client {
	return netx.New(netx.Config{NoBackground: true}).Client("mcp", netx.Tool)
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
	for _, line := range strings.Split(string(data), "\n") {
		if section, ok := mcpSection(line); ok && (section == prefix || strings.HasPrefix(section, prefix+".")) {
			return true
		}
	}
	return false
}

// removeTOMPSection deletes one server block and all of its subtables while
// leaving every other section untouched.
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
