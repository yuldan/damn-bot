//go:build !minimal

package config

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"testing"
)

// TestWebConfigDefaults is SPEC §13.2: the web UI listens on loopback:7878,
// refuses remote binds and uses token auth unless the operator says otherwise.
func TestWebConfigDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, "")
	if err != nil {
		t.Fatal(err)
	}
	want := Web{Listen: "127.0.0.1:7878", AllowedHosts: []string{}, Auth: "token"}
	if cfg.Web.Listen != want.Listen || cfg.Web.AllowRemote ||
		cfg.Web.Auth != want.Auth || !slices.Equal(cfg.Web.AllowedHosts, want.AllowedHosts) ||
		cfg.Web.TLSCert != "" || cfg.Web.TLSKey != "" {
		t.Fatalf("web defaults = %+v, want %+v", cfg.Web, want)
	}
}

// TestWebRemoteRequiresTLS is the GAP-SEC-07 fail-closed gate: a non-loopback
// listener never starts without a TLS certificate pair, even when the
// operator opted in with allow_remote.
func TestWebRemoteRequiresTLS(t *testing.T) {
	t.Parallel()
	_, err := parse(t, "[web]\nlisten = \"0.0.0.0:7878\"\nallow_remote = true\n")
	ke := keyErrorOf(t, err, ErrInvalidValue)
	if ke.Key != "web.allow_remote" {
		t.Fatalf("key = %q, want web.allow_remote", ke.Key)
	}
	// One half of the pair is not a TLS listener either.
	_, err = parse(t, fmt.Sprintf("[web]\nlisten = \"0.0.0.0:7878\"\nallow_remote = true\ntls_cert = %q\n", hostPath(t)))
	if ke := keyErrorOf(t, err, ErrInvalidValue); ke.Key != "web.tls_key" {
		t.Fatalf("half pair key = %q, want web.tls_key", ke.Key)
	}
}

// TestWebRemoteRequiresAllowRemote: a non-loopback bind is refused before the
// TLS check when allow_remote is still false.
func TestWebRemoteRequiresAllowRemote(t *testing.T) {
	t.Parallel()
	for _, listen := range []string{"0.0.0.0:7878", "192.168.1.5:7878", "example.test:7878", ":7878"} {
		_, err := parse(t, fmt.Sprintf("[web]\nlisten = %q\n", listen))
		ke := keyErrorOf(t, err, ErrInvalidValue)
		if ke.Key != "web.listen" {
			t.Fatalf("listen %q: key = %q, want web.listen", listen, ke.Key)
		}
	}
}

// TestWebRemoteWithTLS: allow_remote plus an absolute PEM pair validates.
func TestWebRemoteWithTLS(t *testing.T) {
	t.Parallel()
	cert := hostPath(t)
	cfg, err := parse(t, fmt.Sprintf(
		"[web]\nlisten = \"0.0.0.0:7878\"\nallow_remote = true\ntls_cert = %q\ntls_key = %q\n", cert, cert))
	if err != nil {
		t.Fatalf("remote with TLS: %v", err)
	}
	if !cfg.Web.AllowRemote || cfg.Web.TLSCert != cert {
		t.Fatalf("web = %+v", cfg.Web)
	}
}

// TestWebListenValidated: the bind form is either host:port or unix:<path>
// (GAP-SEC-07); loopback and unix sockets are local, everything else remote.
func TestWebListenValidated(t *testing.T) {
	t.Parallel()
	cases := []struct {
		listen string
		ok     bool
	}{
		{"127.0.0.1:7878", true},
		{"localhost:7878", true},
		{"[::1]:7878", true},
		{"127.0.0.2:8080", true},
		{"unix:<state_dir>/web.sock", true},
		{"unix:/run/damnbot/web.sock", true},
		{"127.0.0.1", false},
		{"127.0.0.1:0", false},
		{"127.0.0.1:65536", false},
		{"127.0.0.1:http", false},
		{"bad host:7878", false},
		{"unix:web.sock", false},
		{"unix:", false},
	}
	for _, tc := range cases {
		_, err := parse(t, fmt.Sprintf("[web]\nlisten = %q\n", tc.listen))
		if tc.ok && err != nil {
			t.Errorf("listen %q: %v", tc.listen, err)
		}
		if !tc.ok && !errors.Is(err, ErrInvalidValue) {
			t.Errorf("listen %q: err = %v, want ErrInvalidValue", tc.listen, err)
		}
	}
}

// TestWebAllowedHostsValidated: the GAP-SEC-08 allowlist takes bare host
// names and IP literals only, lower case, without duplicates.
func TestWebAllowedHostsValidated(t *testing.T) {
	t.Parallel()
	cases := []struct {
		host string
		ok   bool
	}{
		{"damnbot.example", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"a-b.c", true},
		{"https://damnbot.example", false},
		{"damnbot.example:443", false},
		{"*.damnbot.example", false},
		{"Damnbot.example", false},
		{"damn bot.example", false},
		{"", false},
		{"-bad.example", false},
	}
	for _, tc := range cases {
		_, err := parse(t, fmt.Sprintf("[web]\nallowed_hosts = [%q]\n", tc.host))
		if tc.ok && err != nil {
			t.Errorf("host %q: %v", tc.host, err)
		}
		if !tc.ok && !errors.Is(err, ErrInvalidValue) {
			t.Errorf("host %q: err = %v, want ErrInvalidValue", tc.host, err)
		}
	}
	_, err := parse(t, "[web]\nallowed_hosts = [\"a.example\", \"a.example\"]\n")
	if ke := keyErrorOf(t, err, ErrInvalidValue); ke.Key != "web.allowed_hosts[1]" {
		t.Fatalf("duplicate key = %q", ke.Key)
	}
}

// TestWebAuthModes: every mode is accepted, and each one must name its own
// credential source while a mode does not carry another mode's references.
func TestWebAuthModes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		toml string
		key  string
	}{
		{"token", "[web]\nauth = \"token\"\n", ""},
		{"once", "[web]\nauth = \"once\"\n", ""},
		{"unknown", "[web]\nauth = \"magic\"\n", "web.auth"},
		{"basic without credentials", "[web]\nauth = \"basic\"\n", "web.basic_user"},
		{"basic without secret", "[web]\nauth = \"basic\"\nbasic_user = \"owner\"\n", "web.basic_password_secret"},
		{"basic with value auth", "[web]\nauth = \"basic\"\nbasic_user = \"owner:root\"\nbasic_password_secret = \"web_password\"\n", "web.basic_user"},
		{"basic ok", "[web]\nauth = \"basic\"\nbasic_user = \"owner\"\nbasic_password_secret = \"web_password\"\n", ""},
		{"oidc without issuer", "[web]\nauth = \"oidc\"\n", "web.oidc_issuer"},
		{"oidc http remote", "[web]\nauth = \"oidc\"\noidc_issuer = \"http://idp.example\"\noidc_client_id = \"damnbot\"\noidc_client_secret_secret = \"web_oidc\"\n", "web.oidc_issuer"},
		{"oidc userinfo", "[web]\nauth = \"oidc\"\noidc_issuer = \"https://u:p@idp.example\"\noidc_client_id = \"damnbot\"\noidc_client_secret_secret = \"web_oidc\"\n", "web.oidc_issuer"},
		{"oidc without client id", "[web]\nauth = \"oidc\"\noidc_issuer = \"https://idp.example\"\n", "web.oidc_client_id"},
		{"oidc ok", "[web]\nauth = \"oidc\"\noidc_issuer = \"https://idp.example/realms/bot\"\noidc_client_id = \"damnbot\"\noidc_client_secret_secret = \"web_oidc\"\n", ""},
		{"basic refs under token", "[web]\nbasic_user = \"owner\"\n", "web.basic_user"},
		{"oidc refs under once", "[web]\nauth = \"once\"\noidc_issuer = \"https://idp.example\"\n", "web.oidc_issuer"},
	}
	for _, tc := range cases {
		_, err := parse(t, tc.toml)
		if tc.key == "" {
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
			}
			continue
		}
		if ke := keyErrorOf(t, err, ErrInvalidValue); ke.Key != tc.key {
			t.Errorf("%s: key = %q, want %q", tc.name, ke.Key, tc.key)
		}
	}
}

// TestWebUnixListenIsLocal: a unix socket needs no allow_remote, because the
// server authenticates the peer by UID instead of by Host (GAP-SEC-07).
func TestWebUnixListenIsLocal(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, "[web]\nlisten = \"unix:<state_dir>/web.sock\"\n")
	if err != nil {
		t.Fatalf("unix listen: %v", err)
	}
	if cfg.Web.AllowRemote {
		t.Fatalf("allow_remote = true for a local socket")
	}
}

// TestWebRejectsUnknownKey: the section decodes strictly like every other
// one, so a typo cannot silently disable the Host allowlist.
func TestWebRejectsUnknownKey(t *testing.T) {
	t.Parallel()
	_, err := parse(t, "[web]\nallowed_host = [\"a.example\"]\n")
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("err = %v, want ErrUnknownKey", err)
	}
}

// TestWebConfigEnvOverride: scalar keys are env-overridable through the one
// generated name (GAP-OPS-01); slices and TLS never are.
func TestWebConfigEnvOverride(t *testing.T) {
	t.Parallel()
	loader, err := New(Deps{
		ReadFile: func(string) ([]byte, error) { return nil, fs.ErrNotExist },
		Getenv: func(key string) string {
			return map[string]string{
				"DAMNBOT_WEB_AUTH":   "once",
				"DAMNBOT_WEB_LISTEN": "localhost:7979",
			}[key]
		},
		HomeDir: func() (string, error) { return "/home/u", nil },
		GOOS:    "linux",
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := loader.Parse(nil, "config.toml")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := loader.Effective(&cfg); err != nil {
		t.Fatalf("Effective: %v", err)
	}
	if cfg.Web.Auth != "once" || cfg.Web.Listen != "localhost:7979" {
		t.Fatalf("web env override = %+v", cfg.Web)
	}
}

// TestWebCloneIsolatesAllowedHosts: Clone is an independent copy (E-13).
func TestWebCloneIsolatesAllowedHosts(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, "[web]\nallowed_hosts = [\"a.example\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	clone := cfg.Clone()
	clone.Web.AllowedHosts[0] = "b.example"
	if cfg.Web.AllowedHosts[0] != "a.example" {
		t.Fatalf("Clone shared the allowlist: %v", cfg.Web.AllowedHosts)
	}
}
