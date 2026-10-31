//go:build !minimal

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider/auth"
	"github.com/damnbot/damnbot/internal/secret"
)

// setupAuth implements `damnbot setup auth login|logout|status <provider>`
// (SPEC §10, GAP-SEC-18). A token never reaches stdout, stderr, logs, or JSON.
func setupAuth(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		say(stderr, "setup auth: login|logout|status expected\n")
		return 2
	}
	switch args[0] {
	case "login":
		return authLogin(args[1:], stdout, stderr)
	case "logout":
		return authLogout(args[1:], stdout, stderr)
	case "status":
		return authStatus(args[1:], stdout, stderr)
	}
	say(stderr, "setup auth: unknown subcommand %q\n", cleanArg(args[0]))
	return 2
}

// openAuth opens the per-profile token store and OAuth transport. The
// keychain is preferred; headless hosts use <data>/auth (0700, files 0600).
func openAuth(provider, issuer, clientID, clientSecret, scope string, offline bool, out io.Writer) (*auth.Store, *auth.OAuth, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return nil, nil, fmt.Errorf("config deps: %w", err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return nil, nil, fmt.Errorf("config paths: %w", err)
	}
	sec, err := secret.New(secret.Options{ConfigDir: paths.ConfigDir, Profile: activeProfile(), Keyring: setupKeyring()})
	if err != nil {
		return nil, nil, fmt.Errorf("secret store: %w", err)
	}
	store, err := auth.NewStore(sec, filepath.Join(paths.DataDir, "auth"))
	if err != nil {
		return nil, nil, fmt.Errorf("token store: %w", err)
	}
	net := netx.New(netx.Config{Offline: offline, NoBackground: true})
	o, err := auth.NewOAuth(&auth.OAuthConfig{
		Provider: provider, Issuer: issuer, ClientID: clientID,
		ClientSecret: clientSecret, Scopes: strings.Fields(strings.ReplaceAll(scope, ",", " ")),
		Store: store, Client: net.Client("auth", netx.Inference), Output: out, Offline: offline,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("oauth transport: %w", err)
	}
	return store, o, nil
}

func authLogin(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("setup auth login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	issuer := fs.String("issuer", "", "OAuth issuer URL (RFC 8414 discovery)")
	clientID := fs.String("client-id", "", "client id; empty uses dynamic registration")
	clientSecret := fs.String("client-secret", "", "client secret for a confidential client")
	scope := fs.String("scope", "", "space- or comma-separated scopes")
	offline := fs.Bool("offline", false, "refuse network access")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		say(stderr, "setup auth login: one provider expected\n")
		return 2
	}
	provider := fs.Arg(0)
	if *issuer == "" {
		say(stderr, "setup auth login: --issuer is required (registry presets land with F5-01)\n")
		return 2
	}
	_, o, err := openAuth(provider, *issuer, *clientID, *clientSecret, *scope, *offline, stdout)
	if err != nil {
		return authFail(stderr, "login", err)
	}
	tok, err := o.Login(context.Background())
	if err != nil {
		return authFail(stderr, "login", err)
	}
	say(stdout, "login %s: ok (%s)\n", provider, expiryLabel(&tok, time.Now()))
	return 0
}

func authLogout(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("setup auth logout", flag.ContinueOnError)
	fs.SetOutput(stderr)
	offline := fs.Bool("offline", false, "refuse network access")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		say(stderr, "setup auth logout: one provider expected\n")
		return 2
	}
	r := logoutOne(fs.Arg(0), *offline)
	if code := say(stdout, "%s: %s%s\n", r.Provider, r.State, errSuffix(r.Error)); code != 0 {
		return code
	}
	if r.State == "failed" {
		return 1
	}
	return 0
}

// logoutOne revokes first and removes the local token only after that. A
// failed revoke keeps the token for a retry; an unsupported endpoint removes
// it and is reported as such (SPEC §22.1 GAP-SEC-18).
func logoutOne(provider string, offline bool) authResult {
	r := authResult{Provider: provider, State: "failed"}
	store, _, err := openAuth(provider, "", "", "", "", offline, io.Discard)
	if err != nil {
		r.Error = cleanLine(err.Error())
		return r
	}
	tok, err := store.Load(context.Background(), provider)
	if err != nil {
		r.Error = cleanLine(err.Error())
		return r
	}
	_, o, err := openAuth(provider, tok.Issuer, tok.ClientID, tok.ClientSecret, tok.Scope, offline, io.Discard)
	if err == nil {
		err = o.Revoke(context.Background(), &tok)
	}
	switch {
	case errors.Is(err, auth.ErrUnsupported):
		r.State = "unsupported"
	case err != nil:
		r.Error = cleanLine(err.Error())
		return r
	default:
		r.State = "revoked"
	}
	if derr := store.Delete(context.Background(), provider); derr != nil {
		r.State, r.Error = "failed", cleanLine(derr.Error())
	}
	return r
}

func authStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("setup auth status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	asJSON := fs.Bool("json", false, "machine-readable token status")
	offline := fs.Bool("offline", false, "refuse network access")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		say(stderr, "setup auth status: one provider expected\n")
		return 2
	}
	provider := fs.Arg(0)
	store, _, err := openAuth(provider, "", "", "", "", *offline, io.Discard)
	if err != nil {
		return authFail(stderr, "status", err)
	}
	tok, serr := store.Load(context.Background(), provider)
	state := "ok"
	switch {
	case serr != nil:
		state = "failed"
	case tok.Expired(time.Now()) && tok.RefreshToken == "":
		state = "expired"
	case tok.Expired(time.Now()):
		if _, o, oerr := openAuth(provider, tok.Issuer, tok.ClientID, tok.ClientSecret, tok.Scope, *offline, io.Discard); oerr != nil {
			serr = oerr
		} else {
			serr = o.Refresh(context.Background(), &tok)
		}
		if serr != nil {
			state = "failed"
		}
	}
	r := authResult{Provider: provider, State: state, Issuer: tok.Issuer, Scope: tok.Scope, Expiry: expiryLabel(&tok, time.Now())}
	if serr != nil {
		r.Error = cleanLine(serr.Error())
	}
	if code := writeAuthResult(stdout, *asJSON, &r); code != 0 {
		return code
	}
	if state == "failed" {
		return 1
	}
	return 0
}

// authResult is the only status shape printed: it has no token fields, so a
// JSON encoder cannot leak one.
type authResult struct {
	Provider string `json:"provider"`
	State    string `json:"state"`
	Expiry   string `json:"expiry,omitempty"`
	Scope    string `json:"scope,omitempty"`
	Issuer   string `json:"issuer,omitempty"`
	Error    string `json:"error,omitempty"`
}

func writeAuthResult(w io.Writer, asJSON bool, r *authResult) int {
	if asJSON {
		b, err := json.Marshal(r)
		if err != nil {
			say(w, "setup auth: encode result: %s\n", cleanLine(err.Error()))
			return 1
		}
		return say(w, "%s\n", b)
	}
	expiry := ""
	if r.Expiry != "" {
		expiry = " (" + r.Expiry + ")"
	}
	return say(w, "%s: %s%s%s\n", r.Provider, r.State, expiry, errSuffix(r.Error))
}

func expiryLabel(t *auth.Token, now time.Time) string {
	switch {
	case t.Expiry.IsZero():
		return "no expiry"
	case t.Expired(now):
		return "expired " + t.Expiry.UTC().Format(time.RFC3339)
	default:
		return "expires " + t.Expiry.UTC().Format(time.RFC3339)
	}
}

func errSuffix(err string) string {
	if err == "" {
		return ""
	}
	return ": " + err
}

func authFail(stderr io.Writer, cmd string, err error) int {
	say(stderr, "setup auth %s: %s\n", cmd, cleanLine(err.Error()))
	return 1
}
