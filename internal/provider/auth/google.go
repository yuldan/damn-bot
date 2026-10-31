//go:build !minimal

package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
)

// GoogleEnvFile is the ADC variable consulted when CredentialsFile is empty.
const GoogleEnvFile = "GOOGLE_APPLICATION_CREDENTIALS"

// tokenSkew refreshes an access token this long before it expires.
const tokenSkew = 60 * time.Second

// GoogleConfig loads a Google service-account JSON key. LookupEnv is injected
// (the composition root owns process env, GAP-OPS-01); Client is a netx
// egress point (E-05).
type GoogleConfig struct {
	CredentialsFile string
	LookupEnv       func(string) string
	Scopes          []string
	Client          *netx.Client
	Now             func() time.Time
}

// Google exchanges a signed service-account JWT for an OAuth access token and
// caches it until [tokenSkew] before expiry. Safe for concurrent use.
type Google struct {
	mu     sync.Mutex
	cfg    GoogleConfig
	creds  googleCreds
	access string
	expiry time.Time
}

type googleCreds struct {
	Type         string `json:"type"`
	ClientEmail  string `json:"client_email"`
	PrivateKey   string `json:"private_key"`
	PrivateKeyID string `json:"private_key_id"`
	TokenURI     string `json:"token_uri"`
}

// NewGoogle loads and validates the key file; it performs no network I/O.
func NewGoogle(c GoogleConfig) (*Google, error) {
	if c.Client == nil {
		return nil, fmt.Errorf("%w: google needs a netx client", ErrCredential)
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	path := c.CredentialsFile
	if path == "" && c.LookupEnv != nil {
		path = c.LookupEnv(GoogleEnvFile)
	}
	if path == "" {
		return nil, fmt.Errorf("%w: no %s file", ErrCredential, GoogleEnvFile)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-selected ADC file, not request input
	if err != nil {
		return nil, fmt.Errorf("%w: read google credentials: %w", ErrCredential, err)
	}
	g := &Google{cfg: c}
	if err := json.Unmarshal(raw, &g.creds); err != nil {
		return nil, fmt.Errorf("%w: decode google credentials: %w", ErrCredential, err)
	}
	if g.creds.Type != "service_account" || g.creds.ClientEmail == "" || g.creds.PrivateKey == "" {
		return nil, fmt.Errorf("%w: google credentials are not a service account", ErrCredential)
	}
	if g.creds.TokenURI == "" {
		return nil, fmt.Errorf("%w: google credentials have no token_uri", ErrCredential)
	}
	if err := endpointOK(g.creds.TokenURI); err != nil {
		return nil, err
	}
	if _, err := parseRSAKey(g.creds.PrivateKey); err != nil {
		return nil, err
	}
	return g, nil
}

// Sign implements [Signer]. The request body is irrelevant: the Google JWT
// authenticates the client, not the payload.
func (g *Google) Sign(ctx context.Context, req *http.Request, _ []byte) error {
	tok, err := g.token(ctx)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return nil
}

func (g *Google) token(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.access != "" && g.cfg.Now().Add(tokenSkew).Before(g.expiry) {
		return g.access, nil
	}
	assertion, err := g.assertion()
	if err != nil {
		return "", err
	}
	form := url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	}
	resp, err := doForm(ctx, g.cfg.Client, g.creds.TokenURI, form)
	if err != nil {
		return "", err
	}
	if resp.Error != "" {
		return "", fmt.Errorf("auth: google token endpoint: %s", resp.Error)
	}
	if resp.AccessToken == "" {
		return "", fmt.Errorf("%w: google token endpoint returned no access token", ErrCredential)
	}
	ttl := time.Duration(resp.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	g.access, g.expiry = resp.AccessToken, g.cfg.Now().Add(ttl)
	return g.access, nil
}

// assertion builds and RS256-signs the JWT bearer assertion (RFC 7523).
func (g *Google) assertion() (string, error) {
	key, err := parseRSAKey(g.creds.PrivateKey)
	if err != nil {
		return "", err
	}
	now := g.cfg.Now().Unix()
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": g.creds.PrivateKeyID})
	if err != nil {
		return "", fmt.Errorf("auth: google jwt header: %w", err)
	}
	claims, err := json.Marshal(map[string]any{
		"iss": g.creds.ClientEmail, "scope": strings.Join(g.cfg.Scopes, " "),
		"aud": g.creds.TokenURI, "iat": now, "exp": now + 3600,
	})
	if err != nil {
		return "", fmt.Errorf("auth: google jwt claims: %w", err)
	}
	enc := base64.RawURLEncoding
	unsigned := enc.EncodeToString(header) + "." + enc.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("auth: google jwt sign: %w", err)
	}
	return unsigned + "." + enc.EncodeToString(sig), nil
}

func parseRSAKey(pemText string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemText))
	if block == nil {
		return nil, fmt.Errorf("%w: google private key is not PEM", ErrCredential)
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rsaKey, ok := key.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
	}
	return nil, fmt.Errorf("%w: google private key is not RSA", ErrCredential)
}
