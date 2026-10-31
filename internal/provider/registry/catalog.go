package registry

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

// Protocol is the wire adapter a catalog entry speaks (SPEC §4.1).
type Protocol string

// Catalog protocols. A provider may list several (e.g. Copilot); every
// protocol must have an adapter before a model of that provider can run.
const (
	ProtocolOpenAIChat        Protocol = "openai-chat"
	ProtocolOpenAIResponses   Protocol = "openai-responses"
	ProtocolAnthropicMessages Protocol = "anthropic-messages"
	ProtocolGemini            Protocol = "gemini"
	ProtocolBedrock           Protocol = "bedrock"
	ProtocolCursor            Protocol = "cursor"
	ProtocolFactory           Protocol = "factory"
	ProtocolOllama            Protocol = "ollama"
	ProtocolApple             Protocol = "apple"
)

func knownProtocol(p Protocol) bool {
	switch p {
	case ProtocolOpenAIChat, ProtocolOpenAIResponses, ProtocolAnthropicMessages, ProtocolGemini,
		ProtocolBedrock, ProtocolCursor, ProtocolFactory, ProtocolOllama, ProtocolApple:
		return true
	default:
		return false
	}
}

// Auth is how a catalog entry proves its identity to the endpoint.
type Auth string

// Catalog auth kinds. "unknown" is honest for an omp-only row whose auth the
// evidence did not establish; it is not a synonym for "none".
const (
	AuthAPIKey      Auth = "api-key"
	AuthOAuth       Auth = "oauth"
	AuthGitHubToken Auth = "github-token"
	AuthAWSIAM      Auth = "aws-iam"
	AuthGoogleADC   Auth = "google-adc"
	AuthACP         Auth = "acp"
	AuthNone        Auth = "none"
	AuthUnknown     Auth = "unknown"
)

// Capability is a provider feature the runtime may gate on (SPEC §4.1).
type Capability string

// Catalog capabilities. Empty means the evidence did not establish any; the
// runtime must not infer one from another.
const (
	CapTools     Capability = "tools"
	CapVision    Capability = "vision"
	CapReasoning Capability = "reasoning"
	CapCache     Capability = "cache"
)

// Pricing is the price provenance class of a catalog entry. The per-route
// [[price]] table stays authoritative; a catalog entry marked unpriced has no
// route price, so a monetary cap fails closed (ADR-123).
type Pricing string

// Pricing classes. Only "vendor" and "operator-contracted" match PriceKind.
const (
	PricingUnpriced Pricing = "unpriced"
	PricingVendor   Pricing = "vendor"
	PricingOperator Pricing = "operator-contracted"
)

// CatalogLimits is the conservative provider default of SPEC §4.1. Zero
// means unknown and is resolved (never guessed) by the runtime.
type CatalogLimits struct {
	ContextWindow int64 `toml:"context_window"`
	MaxOutput     int64 `toml:"max_output"`
	MaxImageBytes int64 `toml:"max_image_bytes"`
}

// Provider is one validated catalog row.
type Provider struct {
	ID           string
	Name         string
	Protocols    []Protocol
	BaseURL      string // empty: user-supplied or a terminal-only constant
	Auth         Auth
	Env          []string // credential env candidates in source order
	Aliases      []string
	Capabilities []Capability
	Attribution  []string // header names; never sent unless SEC-25 opt-in
	CacheTTLMax  int64    // seconds of explicit cache_ttl support; 0 = none claimed
	Limits       CatalogLimits
	Pricing      Pricing
	Local        bool
}

// Catalog is the immutable provider catalog of the embedded providers.toml.
// Lookup accepts an id or an alias. Safe to share; callers treat slices as
// read-only.
type Catalog struct {
	all       []Provider
	byName    map[string]int // lowercased id or alias -> index in all
	providers int            // non-local rows
}

// ErrCatalog rejects an invalid catalog. The composition root must fail at
// startup: a typo must not silently turn a provider into "unknown".
var ErrCatalog = errors.New("registry: invalid provider catalog")

// DefaultCatalog parses the embedded providers.toml once.
func DefaultCatalog() (*Catalog, error) { return defaultCatalog() }

var defaultCatalog = sync.OnceValues(func() (*Catalog, error) { return ParseCatalog(providersTOML) }) //nolint:gochecknoglobals // E-10 allowlist: embedded catalog parsed once, read-only (ENGINEERING §2.3).

// catalogFile and catalogEntry are the TOML shape of the catalog. The price
// rows belong to ParsePriceTable and are decoded as opaque values so unknown
// keys stay an error in both parsers without duplicating the other schema.
type catalogFile struct {
	Version  string         `toml:"version"`
	Provider []catalogEntry `toml:"provider"`
	Local    []catalogEntry `toml:"local"`
	Price    []any          `toml:"price"`
}

type catalogEntry struct {
	ID           string        `toml:"id"`
	Name         string        `toml:"name"`
	Protocols    []string      `toml:"protocols"`
	BaseURL      string        `toml:"base_url"`
	Auth         string        `toml:"auth"`
	Env          []string      `toml:"env"`
	Aliases      []string      `toml:"aliases"`
	Capabilities []string      `toml:"capabilities"`
	Attribution  []string      `toml:"attribution"`
	CacheTTLMax  int64         `toml:"cache_ttl_max"`
	Limits       CatalogLimits `toml:"limits"`
	Pricing      string        `toml:"prices"`
}

// firstUndecoded returns the first undecoded key outside the tables another
// parser owns (the price rows and the provider catalog share providers.toml).
func firstUndecoded(md *toml.MetaData, skip ...string) (toml.Key, bool) {
	for _, key := range md.Undecoded() {
		if len(key) > 0 && slices.Contains(skip, key[0]) {
			continue
		}
		return key, true
	}
	return nil, false
}

// ParseCatalog decodes and validates a providers.toml catalog. It rejects
// unknown keys, an unknown protocol/auth/capability/pricing, an invalid or
// duplicate id/alias (ids and aliases share one namespace, compared without
// case), a duplicate protocol within one entry, and a negative bound.
func ParseCatalog(data []byte) (*Catalog, error) {
	var f catalogFile
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCatalog, err)
	}
	if und, ok := firstUndecoded(&md, "price"); ok {
		return nil, fmt.Errorf("%w: unknown key %q", ErrCatalog, und.String())
	}
	if strings.TrimSpace(f.Version) == "" {
		return nil, fmt.Errorf("%w: missing table version", ErrCatalog)
	}
	c := &Catalog{
		all:    make([]Provider, 0, len(f.Provider)+len(f.Local)),
		byName: make(map[string]int, len(f.Provider)+len(f.Local)),
	}
	for i := range f.Provider {
		if err := c.add(&f.Provider[i], false); err != nil {
			return nil, fmt.Errorf("%w: provider %q: %w", ErrCatalog, f.Provider[i].ID, err)
		}
	}
	for i := range f.Local {
		if err := c.add(&f.Local[i], true); err != nil {
			return nil, fmt.Errorf("%w: local %q: %w", ErrCatalog, f.Local[i].ID, err)
		}
	}
	c.providers = len(c.all) - len(f.Local)
	return c, nil
}

// add validates one entry, appends it, and indexes its id and aliases.
func (c *Catalog) add(e *catalogEntry, local bool) error {
	p, err := e.parse(local)
	if err != nil {
		return err
	}
	for _, name := range append([]string{p.ID}, p.Aliases...) {
		key := strings.ToLower(name)
		if prev, ok := c.byName[key]; ok {
			return fmt.Errorf("name %q already belongs to %q", name, c.all[prev].ID)
		}
	}
	c.byName[strings.ToLower(p.ID)] = len(c.all)
	c.all = append(c.all, p)
	for _, alias := range p.Aliases {
		c.byName[strings.ToLower(alias)] = len(c.all) - 1
	}
	return nil
}

// parse validates one row and returns its typed form.
func (e *catalogEntry) parse(local bool) (Provider, error) {
	if !catalogName(e.ID) {
		return Provider{}, fmt.Errorf("id %q is not [a-z0-9._-]+", e.ID)
	}
	name := strings.TrimSpace(e.Name)
	if name == "" || len(name) > 128 || strings.ContainsAny(name, "\r\n\t") {
		return Provider{}, fmt.Errorf("name %q is empty or not printable", e.Name)
	}
	p, err := e.fields()
	if err != nil {
		return Provider{}, err
	}
	p.ID, p.Name, p.Local = e.ID, name, local
	return p, nil
}

// fields validates the declarative columns of one row.
func (e *catalogEntry) fields() (Provider, error) {
	protocols, err := e.protocols()
	if err != nil {
		return Provider{}, err
	}
	auth, err := parseAuth(e.Auth)
	if err != nil {
		return Provider{}, err
	}
	if e.BaseURL != "" && !templateURL(e.BaseURL) {
		return Provider{}, fmt.Errorf("base_url %q is not http(s)://, acp://, or empty", e.BaseURL)
	}
	env, err := e.env()
	if err != nil {
		return Provider{}, err
	}
	aliases, err := e.aliases()
	if err != nil {
		return Provider{}, err
	}
	caps, err := e.capabilities()
	if err != nil {
		return Provider{}, err
	}
	headers, err := e.attribution()
	if err != nil {
		return Provider{}, err
	}
	if e.CacheTTLMax < 0 {
		return Provider{}, fmt.Errorf("cache_ttl_max %d is negative", e.CacheTTLMax)
	}
	lim := e.Limits
	if lim.ContextWindow < 0 || lim.MaxOutput < 0 || lim.MaxImageBytes < 0 {
		return Provider{}, errors.New("negative limits")
	}
	pricing, err := parsePricing(e.Pricing)
	if err != nil {
		return Provider{}, err
	}
	return Provider{
		Protocols: protocols, BaseURL: e.BaseURL, Auth: auth, Env: env, Aliases: aliases,
		Capabilities: caps, Attribution: headers, CacheTTLMax: e.CacheTTLMax,
		Limits: lim, Pricing: pricing,
	}, nil
}

func (e *catalogEntry) protocols() ([]Protocol, error) {
	if len(e.Protocols) == 0 {
		return nil, errors.New("missing protocols")
	}
	out := make([]Protocol, 0, len(e.Protocols))
	for _, raw := range e.Protocols {
		p := Protocol(strings.TrimSpace(raw))
		if !knownProtocol(p) {
			return nil, fmt.Errorf("unknown protocol %q", raw)
		}
		if slices.Contains(out, p) {
			return nil, fmt.Errorf("duplicate protocol %q", raw)
		}
		out = append(out, p)
	}
	return out, nil
}

func parseAuth(raw string) (Auth, error) {
	auth := Auth(strings.TrimSpace(raw))
	switch auth {
	case AuthAPIKey, AuthOAuth, AuthGitHubToken, AuthAWSIAM, AuthGoogleADC, AuthACP, AuthNone, AuthUnknown:
		return auth, nil
	default:
		return "", fmt.Errorf("unknown auth %q", raw)
	}
}

// env validates the credential env candidates: uppercase names only, no
// duplicates, never a value.
func (e *catalogEntry) env() ([]string, error) {
	out := make([]string, 0, len(e.Env))
	for _, raw := range e.Env {
		name := strings.TrimSpace(raw)
		if !envName(name) {
			return nil, fmt.Errorf("env %q is not [A-Z][A-Z0-9_]*", raw)
		}
		if slices.Contains(out, name) {
			return nil, fmt.Errorf("duplicate env %q", name)
		}
		out = append(out, name)
	}
	return out, nil
}

func (e *catalogEntry) aliases() ([]string, error) {
	out := make([]string, 0, len(e.Aliases))
	for _, raw := range e.Aliases {
		alias := strings.TrimSpace(raw)
		if !catalogName(alias) {
			return nil, fmt.Errorf("alias %q is not [a-z0-9._-]+", raw)
		}
		if slices.Contains(out, alias) {
			return nil, fmt.Errorf("duplicate alias %q", alias)
		}
		out = append(out, alias)
	}
	return out, nil
}

func (e *catalogEntry) capabilities() ([]Capability, error) {
	out := make([]Capability, 0, len(e.Capabilities))
	for _, raw := range e.Capabilities {
		cap := Capability(strings.TrimSpace(raw))
		switch cap {
		case CapTools, CapVision, CapReasoning, CapCache:
		default:
			return nil, fmt.Errorf("unknown capability %q", raw)
		}
		if slices.Contains(out, cap) {
			return nil, fmt.Errorf("duplicate capability %q", raw)
		}
		out = append(out, cap)
	}
	return out, nil
}

func (e *catalogEntry) attribution() ([]string, error) {
	out := make([]string, 0, len(e.Attribution))
	for _, raw := range e.Attribution {
		h := strings.TrimSpace(raw)
		if !headerName(h) {
			return nil, fmt.Errorf("attribution header %q is not [A-Za-z0-9-]+", raw)
		}
		if slices.Contains(out, h) {
			return nil, fmt.Errorf("duplicate attribution header %q", h)
		}
		out = append(out, h)
	}
	return out, nil
}

func parsePricing(raw string) (Pricing, error) {
	switch p := Pricing(strings.TrimSpace(raw)); p {
	case "":
		return PricingUnpriced, nil
	case PricingUnpriced, PricingVendor, PricingOperator:
		return p, nil
	default:
		return "", fmt.Errorf("unknown prices %q", raw)
	}
}

// Lookup returns the entry named by id or alias, case-insensitively.
func (c *Catalog) Lookup(name string) (Provider, bool) {
	if c == nil {
		return Provider{}, false
	}
	i, ok := c.byName[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return Provider{}, false
	}
	return c.all[i], true
}

// Providers returns every row in TOML order (88 providers, then locals).
func (c *Catalog) Providers() []Provider { return slices.Clone(c.all) }

// Len is the number of rows including local servers.
func (c *Catalog) Len() int { return len(c.all) }

// ProviderCount is the number of non-local providers (the 88 of PROVIDERS.md).
func (c *Catalog) ProviderCount() int { return c.providers }

// catalogName accepts the id and alias grammar of PROVIDERS.md and config:
// lowercase letters, digits, '.', '_', '-'. No slash, whitespace, or case.
func catalogName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// templateURL accepts the base_url forms the catalog has to carry: a plain
// http(s)/acp URL, possibly with a documented {region} or <account>
// placeholder. It never resolves the URL; the runtime refuses placeholders.
func templateURL(s string) bool {
	if strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	for _, scheme := range []string{"http://", "https://", "acp://"} {
		if rest, ok := strings.CutPrefix(s, scheme); ok {
			return rest != ""
		}
	}
	return false
}

// envName is the credential variable grammar: an environment variable name,
// never a secret value or a shell expression.
func envName(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r == '_':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// headerName is the HTTP field-name token grammar (RFC 9110 tchar subset).
func headerName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}
