package config

// Web authentication modes (SPEC §13.2, GAP-SEC-07). Token is the loopback
// session token printed by `serve --web`; Once accepts only one-use link
// exchanges; Basic and OIDC are the remote credential modes.
const (
	WebAuthToken = "token"
	WebAuthOnce  = "once"
	WebAuthBasic = "basic"
	WebAuthOIDC  = "oidc"
)

// DefaultWebListen is the SPEC §13.2 loopback bind. The web UI never listens
// on a public interface unless the operator opts in (GAP-SEC-08).
const DefaultWebListen = "127.0.0.1:7878"

// Web is SPEC §13.2 [web]: the `damnbot serve --web` listener (F8). The
// section is fail-closed: a non-loopback bind needs allow_remote *and* a TLS
// certificate pair, and every auth mode needs its credential source named
// before the listener starts.
//
// The type is declared in every build so a shared config file keeps decoding
// identically; only the fail-closed validation is full-build code, because
// the minimal binary has no web surface to start (ADR-325).
type Web struct {
	// Listen is the bind address, either "host:port" or "unix:<path>"
	// (GAP-SEC-07). The default is loopback only.
	Listen string `toml:"listen" json:"listen"`
	// AllowRemote permits a non-loopback Listen. False refuses one at
	// validation time instead of exposing the UI on a public interface.
	AllowRemote bool `toml:"allow_remote" json:"allow_remote"`
	// AllowedHosts is the Host allowlist for a non-loopback bind
	// (GAP-SEC-08): bare host names or IP literals, no scheme, port or
	// wildcard. Empty means "the TLS certificate host".
	AllowedHosts []string `toml:"allowed_hosts" json:"allowed_hosts"`
	// Auth is one of WebAuthToken, WebAuthOnce, WebAuthBasic, WebAuthOIDC.
	Auth string `toml:"auth" json:"auth"`
	// TLSCert and TLSKey are the PEM pair used when the listener terminates
	// TLS itself; a non-loopback bind requires both.
	TLSCert string `toml:"tls_cert" json:"tls_cert"`
	TLSKey  string `toml:"tls_key" json:"tls_key"`
	// BasicUser and BasicPasswordSecret name the single basic-auth
	// principal; the password is a keychain secret name, never a value.
	BasicUser           string `toml:"basic_user" json:"basic_user"`
	BasicPasswordSecret string `toml:"basic_password_secret" json:"basic_password_secret"`
	// OIDCIssuer, OIDCClientID and OIDCClientSecretSecret configure the OIDC
	// seam: an https issuer URL plus a keychain secret name for the client
	// secret.
	OIDCIssuer             string `toml:"oidc_issuer" json:"oidc_issuer"`
	OIDCClientID           string `toml:"oidc_client_id" json:"oidc_client_id"`
	OIDCClientSecretSecret string `toml:"oidc_client_secret_secret" json:"oidc_client_secret_secret"`
}

// defaultWeb is SPEC §13.2: loopback listener, remote refused, token auth and
// no credential references.
func defaultWeb() Web {
	return Web{Listen: DefaultWebListen, AllowedHosts: []string{}, Auth: WebAuthToken}
}
