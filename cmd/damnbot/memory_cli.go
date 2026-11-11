//go:build !minimal

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/memoryproviders"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/secret"
)

const (
	memorySection       = "[modules.memory]"
	memoryHonchoSection = "[modules.memory.honcho]"
)

// runMemory implements `damnbot memory` (CLI-052, MEM-005/008/010). Config
// only ever names a keychain entry; no token value is written or printed.
func runMemory(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config path")
	dataDir := fs.String("data-dir", "", "data directory (default: profile data dir)")
	if err := fs.Parse(args); err != nil || fs.NArg() == 0 {
		return f8ExitUsage
	}
	rest := fs.Args()
	if rest[0] == "status" {
		if len(rest) != 1 {
			return f8ExitUsage
		}
		return memoryStatus(*cfgPath, *dataDir, stdout, stderr)
	}
	switch rest[0] {
	case "setup":
		sec, serr := secretsStore()
		if serr != nil {
			return f8CmdFail(stderr, "memory setup", f8Unavail("memory", "the credential store is unavailable"))
		}
		return memorySetup(rest[1:], *cfgPath, sec, stdout, stderr)
	case "off":
		return memoryOff(rest[1:], *cfgPath, stdout, stderr)
	case "reset":
		sec, serr := secretsStore()
		if serr != nil {
			sec = nil
		}
		return memoryReset(rest[1:], *cfgPath, sec, stdout, stderr)
	default:
		say(stderr, "memory: unknown subcommand %q\n", cleanArg(rest[0]))
		return f8ExitUsage
	}
}

func memoryUsage(w io.Writer) {
	say(w, "usage: damnbot memory [--config FILE] status\n")
	say(w, "       damnbot memory setup --provider NAME (--token-file FILE | --code CODE --verifier V --token-url URL --client-id ID | --token-secret NAME)\n")
	say(w, "             [--base-url URL] [--workspace W] [--session-id S] [--peer-id P] [--token-secret NAME]\n")
	say(w, "       damnbot memory off\n")
	say(w, "       damnbot memory reset\n")
}

func memoryStatus(cfgPath, dataDir string, stdout, stderr io.Writer) int {
	cfg, _, _, err := f8LoadSetup(cfgPath, dataDir)
	if err != nil {
		return f8CmdFail(stderr, "memory", err)
	}
	m := cfg.Modules.Memory
	active := m.Provider == memoryproviders.SourceHoncho && m.Honcho.Enabled
	name := m.Provider
	if name == "" {
		name = "none"
	}
	say(stdout, "provider %s\nactive %t\nembeddings %s\n", cleanLine(name), active, cleanLine(cfg.Memory.Embeddings))
	if active {
		say(stdout, "token_secret %s\nbase_url %s\n", cleanLine(m.Honcho.TokenSecret), cleanLine(m.Honcho.BaseURL))
	}
	return f8ExitOK
}

func memorySetup(args []string, cfgPath string, sec memoryproviders.SecretStore, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	provider := fs.String("provider", "", "provider name (honcho)")
	tokenFile := fs.String("token-file", "", "0600 file holding an API token to store")
	code := fs.String("code", "", "OAuth authorization code")
	verifier := fs.String("verifier", "", "PKCE code verifier")
	tokenURL := fs.String("token-url", "", "OAuth token endpoint")
	clientID := fs.String("client-id", "", "OAuth client id")
	clientSecret := fs.String("client-secret-secret", "", "keychain entry holding the OAuth client secret")
	tokenSecret := fs.String("token-secret", "", "keychain entry name (default memory_<provider>)")
	baseURL := fs.String("base-url", "", "provider base URL (https or loopback)")
	workspace := fs.String("workspace", "", "workspace id")
	sessionID := fs.String("session-id", "", "session id")
	peerID := fs.String("peer-id", "", "peer id")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		memoryUsage(stderr)
		return f8ExitUsage
	}
	if *provider == "" {
		memoryUsage(stderr)
		return f8ExitUsage
	}
	if _, err := memoryproviders.Lookup(*provider); err != nil {
		return f8CmdFail(stderr, "memory setup", f8BadArg("%s", cleanLine(err.Error())))
	}
	name := *tokenSecret
	if name == "" {
		name = "memory_" + *provider
	}
	if !validMemorySecretName(name) {
		return f8CmdFail(stderr, "memory setup", f8BadArg("token secret name %q is invalid", cleanArg(name)))
	}
	ctx := context.Background()
	token, err := memorySetupToken(ctx, sec, &memorySetupOptions{
		TokenFile: *tokenFile, Code: *code, Verifier: *verifier, TokenURL: *tokenURL,
		ClientID: *clientID, ClientSecret: *clientSecret, Name: name,
	})
	if err != nil {
		return f8CmdFail(stderr, "memory setup", err)
	}
	if token != "" {
		setup := &memoryproviders.Setup{Secrets: sec}
		if _, serr := setup.Save(ctx, name, memoryproviders.Token{Access: token}); serr != nil {
			return f8CmdFail(stderr, "memory setup", memorySecretFault("save", serr))
		}
	}
	memAttrs := [][2]string{{"provider", fmt.Sprintf("%q", *provider)}}
	honchoAttrs := [][2]string{
		{"enabled", "true"},
		{"token_secret", fmt.Sprintf("%q", name)},
	}
	for _, kv := range [][2]string{
		{"base_url", *baseURL},
		{"workspace", *workspace},
		{"session_id", *sessionID},
		{"peer_id", *peerID},
	} {
		if kv[1] != "" {
			honchoAttrs = append(honchoAttrs, [2]string{kv[0], fmt.Sprintf("%q", kv[1])})
		}
	}
	path, err := memoryWriteConfig(cfgPath, memAttrs, honchoAttrs)
	if err != nil {
		return f8CmdFail(stderr, "memory setup", err)
	}
	say(stdout, "memory setup %s: %s written (token in keychain entry %s)\n", cleanLine(*provider), path, cleanLine(name))
	return f8ExitOK
}

type memorySetupOptions struct {
	TokenFile    string
	Code         string
	Verifier     string
	TokenURL     string
	ClientID     string
	ClientSecret string
	Name         string
}

// memorySetupToken returns a token only when the operator supplied one; an
// existing --token-secret reference is validated and left untouched.
func memorySetupToken(ctx context.Context, sec memoryproviders.SecretStore, o *memorySetupOptions) (string, error) {
	switch {
	case o.TokenFile != "":
		b, err := memoryReadTokenFile(o.TokenFile)
		if err != nil {
			return "", err
		}
		return b, nil
	case o.Code != "":
		if o.Verifier == "" || o.TokenURL == "" || o.ClientID == "" {
			return "", f8BadArg("OAuth setup needs --verifier, --token-url and --client-id")
		}
		if err := memoryEndpointOK(o.TokenURL); err != nil {
			return "", err
		}
		net := netx.New(netx.Config{NoBackground: true})
		setup := &memoryproviders.Setup{
			HTTP: net.Client("auth", netx.Inference), Secrets: sec,
			TokenURL: o.TokenURL, ClientID: o.ClientID, ClientSecretName: o.ClientSecret,
		}
		tok, err := setup.Exchange(ctx, o.Code, o.Verifier)
		if err != nil {
			return "", f8BadArg("%s", cleanLine(err.Error()))
		}
		return tok.Access, nil
	case o.Name != "":
		if _, err := sec.Get(ctx, o.Name); err != nil {
			return "", f8Unavail("memory", "keychain entry "+cleanArg(o.Name)+" is missing")
		}
		return "", nil
	}
	return "", f8BadArg("one of --token-file, --code or --token-secret is required")
}

func memoryReadTokenFile(path string) (string, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", f8BadArg("token file: %s", cleanLine(err.Error()))
	}
	if !fi.Mode().IsRegular() || (runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0) {
		return "", f8BadArg("token file must be a regular file only readable by its owner")
	}
	b, err := os.ReadFile(path) // #nosec G304 -- operator-supplied token file
	if err != nil {
		return "", f8BadArg("token file: %s", cleanLine(err.Error()))
	}
	token := strings.TrimSpace(string(b))
	if token == "" || len(token) > 4096 {
		return "", f8BadArg("token file must hold one non-empty token")
	}
	return token, nil
}

func memoryOff(args []string, cfgPath string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		memoryUsage(stderr)
		return f8ExitUsage
	}
	path, err := memoryWriteConfig(cfgPath, [][2]string{{"provider", `"off"`}}, [][2]string{{"enabled", "false"}})
	if err != nil {
		return f8CmdFail(stderr, "memory off", err)
	}
	say(stdout, "memory off: %s written; the credential stays in the keychain\n", path)
	return f8ExitOK
}

func memoryReset(args []string, cfgPath string, sec memoryproviders.SecretStore, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		memoryUsage(stderr)
		return f8ExitUsage
	}
	cfg, _, _, err := f8LoadSetup(cfgPath, "")
	if err != nil {
		return f8CmdFail(stderr, "memory reset", err)
	}
	name := cfg.Modules.Memory.Honcho.TokenSecret
	state := "no credential"
	if name != "" && sec != nil {
		derr := sec.Delete(context.Background(), name)
		switch {
		case derr == nil:
			state = "credential revoked"
		case errors.Is(derr, secret.ErrKeyMissing):
			state = "credential already absent"
		default:
			return f8CmdFail(stderr, "memory reset", memorySecretFault("revoke", derr))
		}
	}
	path, err := memoryWriteConfig(cfgPath,
		[][2]string{{"provider", `"off"`}},
		[][2]string{{"enabled", "false"}, {"token_secret", `""`}, {"base_url", `""`}})
	if err != nil {
		return f8CmdFail(stderr, "memory reset", err)
	}
	say(stdout, "memory reset: %s written; %s\n", path, state)
	return f8ExitOK
}

func memoryWriteConfig(cfgPath string, memAttrs, honchoAttrs [][2]string) (string, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return "", fmt.Errorf("config deps: %w", err)
	}
	path := cfgPath
	if path == "" {
		paths, perr := loader.Paths(activeProfile())
		if perr != nil {
			return "", fmt.Errorf("config paths: %w", perr)
		}
		path = paths.ConfigFile()
	}
	data, rerr := os.ReadFile(path) // #nosec G304 -- operator-selected config path
	if rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		return "", fmt.Errorf("read config: %w", rerr)
	}
	data = upsertSection(data, memorySection, memAttrs)
	data = upsertSection(data, memoryHonchoSection, honchoAttrs)
	if _, perr := loader.Parse(data, path); perr != nil {
		return "", f8BadArg("%s", cleanLine(perr.Error()))
	}
	if merr := os.MkdirAll(filepath.Dir(path), 0o700); merr != nil {
		return "", fmt.Errorf("config dir: %w", merr)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config.toml.*")
	if err != nil {
		return "", fmt.Errorf("config temp: %w", err)
	}
	name := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		memoryCleanup(tmp, name)
		return "", fmt.Errorf("config chmod: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		memoryCleanup(tmp, name)
		return "", fmt.Errorf("config write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		memoryRemove(name)
		return "", fmt.Errorf("config close: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		memoryRemove(name)
		return "", fmt.Errorf("config commit: %w", err)
	}
	return path, nil
}

// memoryCleanup discards a temp config after a failed write. Cleanup errors
// are not actionable on the failure path.
func memoryCleanup(tmp *os.File, name string) {
	if err := tmp.Close(); err != nil {
		return
	}
	memoryRemove(name)
}

func memoryRemove(name string) {
	if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
}

func memorySecretFault(op string, err error) error {
	if errors.Is(err, secret.ErrKeyUnavailable) {
		return f8Unavail("memory", "the OS keychain is unavailable; cannot "+op+" the credential")
	}
	return f8BadArg("credential %s: %s", op, cleanLine(err.Error()))
}

func validMemorySecretName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

func memoryEndpointOK(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return f8BadArg("token endpoint is not a URL")
	}
	if u.Scheme == "https" || (u.Scheme == "http" && proxyLoopback(u.Hostname())) {
		return nil
	}
	return f8BadArg("token endpoint must be https or a loopback http URL")
}
