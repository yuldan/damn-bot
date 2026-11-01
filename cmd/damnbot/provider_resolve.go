package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

// attributionReferer and attributionTitle are the only values damnbot sends
// when provider.send_attribution = true (SEC-25). Default false sends none.
const (
	attributionReferer = "https://github.com/yuldan/damn-bot"
	attributionTitle   = "damnbot"
)

// ErrProviderResolve rejects a provider/model reference the catalog and the
// [[providers.custom]] entries cannot resolve.
var ErrProviderResolve = errors.New("provider resolve")

// providerResolver maps a "provider/model" reference to a wire route. It
// reads the embedded catalog plus the operator's custom providers; it never
// probes the network and never invents a base URL.
type providerResolver struct {
	cfg     *config.Config
	catalog *registry.Catalog
	table   registry.PriceTable
	getenv  func(string) string
	confirm bool // --confirm-base-url: accepted a non-default override base_url
	attrib  bool // provider.send_attribution
	// Test/eval seams: an explicit --base-url/--api-key (or the acpe2e
	// binary's DAMNBOT_E2E_BASE_URL) override the catalog without touching
	// the config file.
	overrideBase, overrideKey string
	e2eBase                   string
	resolverCredentials
}

// newRunResolver builds the resolver over the embedded catalog.
func newRunResolver(cfg *config.Config, table registry.PriceTable, o *runOptions) (*providerResolver, error) {
	catalog, err := registry.DefaultCatalog()
	if err != nil {
		return nil, fmt.Errorf("provider catalog: %w", err)
	}
	return newResolver(cfg, table, catalog, o), nil
}

// newResolver is the injectable form used by tests.
func newResolver(cfg *config.Config, table registry.PriceTable, catalog *registry.Catalog, o *runOptions) *providerResolver {
	return &providerResolver{
		cfg: cfg, catalog: catalog, table: table, getenv: config.OS().Getenv,
		confirm: o.confirmBaseURL, attrib: cfg.Provider.SendAttribution,
		overrideBase: o.baseURL, overrideKey: o.apiKey, e2eBase: e2eProviderBase(),
	}
}

// resolve returns the route for ref. A custom provider that shadows a
// built-in id without override=true is refused; so is a non-default base_url
// for an override unless --confirm-base-url was passed (SEC-24).
func (r *providerResolver) resolve(ctx context.Context, ref string) (provider.Route, error) {
	provName, modelName, ok := strings.Cut(ref, "/")
	if !ok || provName == "" || modelName == "" {
		return provider.Route{}, fmt.Errorf("%w: %q is not provider/model", ErrProviderResolve, ref)
	}
	builtin, builtinOK := r.catalog.Lookup(provName)
	custom := r.customProvider(provName)
	switch {
	case custom != nil:
		return r.resolveCustom(ctx, modelName, custom, &builtin, builtinOK)
	case builtinOK:
		return r.resolveBuiltin(provName, modelName, &builtin)
	default:
		return provider.Route{}, fmt.Errorf("%w: unknown provider %q (not in the catalog and no [[providers.custom]] entry)", ErrProviderResolve, provName)
	}
}

// resolveCustom resolves one [[providers.custom]] row. Only fields with a
// wired runtime consumer are accepted; an unsupported field is a startup
// error rather than a silent no-op.
func (r *providerResolver) resolveCustom(ctx context.Context, modelName string, c *config.CustomProvider, builtin *registry.Provider, builtinOK bool) (provider.Route, error) {
	if err := validateCustom(c, modelName, builtinOK); err != nil {
		return provider.Route{}, err
	}
	base, err := r.customBase(c, builtin, builtinOK)
	if err != nil {
		return provider.Route{}, err
	}
	key, err := r.keyFor(c, builtin, builtinOK)
	if err != nil {
		return provider.Route{}, err
	}
	if builtinOK && !sameBaseURL(base, builtin.BaseURL) {
		slog.WarnContext(ctx, "provider override uses a non-default base_url", "provider", c.Name, "base_url", base)
	}
	var attribution []string
	if builtinOK {
		attribution = builtin.Attribution
	}
	var protocols []registry.Protocol
	if c.API != "" {
		protocols = []registry.Protocol{registry.Protocol(c.API)}
	}
	route, err := r.routeFor(c.Name, modelName, base, key, c.ExtraHeaders, c.ExtraBody, attribution, protocols)
	if err != nil {
		return provider.Route{}, err
	}
	if builtinOK {
		route.CacheTTLMax = builtin.CacheTTLMax
	}
	if err := r.attachCredentialPool(ctx, &route, c); err != nil {
		return provider.Route{}, err
	}
	return route, nil
}

// validateCustom is the SEC-24 + adapter gate of one custom entry.
func validateCustom(c *config.CustomProvider, modelName string, builtinOK bool) error {
	if builtinOK && !c.Override {
		return fmt.Errorf("%w: provider %q is built in; set override = true in [[providers.custom]] to replace it", ErrProviderResolve, c.Name)
	}
	protocol := c.API
	if protocol == "" {
		protocol = string(registry.ProtocolOpenAIChat)
	}
	switch protocol {
	case string(registry.ProtocolOpenAIChat):
	case string(registry.ProtocolOpenAIResponses), string(registry.ProtocolAnthropicMessages), string(registry.ProtocolGemini):
		if err := adapterGate(registry.Protocol(protocol)); err != nil {
			return fmt.Errorf("%w: custom provider %q: %w", ErrProviderResolve, c.Name, err)
		}
	default:
		return fmt.Errorf("%w: custom provider %q speaks %q; only openai-chat, openai-responses, anthropic-messages, and gemini are wired", ErrProviderResolve, c.Name, protocol)
	}
	if !slicesContainsModel(c.Models, modelName) {
		return fmt.Errorf("%w: model %q is not in providers.custom[%q].models", ErrProviderResolve, c.Name, modelName)
	}
	return checkCustomFields(c)
}

// customBase applies the base_url precedence: config entry, built-in
// default for an override, then the test/eval seam.
func (r *providerResolver) customBase(c *config.CustomProvider, builtin *registry.Provider, builtinOK bool) (string, error) {
	base := c.BaseURL
	if base == "" && builtinOK {
		base = builtin.BaseURL
	}
	if base == "" {
		return "", fmt.Errorf("%w: custom provider %q needs base_url", ErrProviderResolve, c.Name)
	}
	if r.overrideBase != "" {
		return r.overrideBase, nil
	}
	if r.e2eBase != "" {
		return r.e2eBase, nil
	}
	if builtinOK && builtin.BaseURL != "" && !r.confirm && !sameBaseURL(base, builtin.BaseURL) {
		return "", fmt.Errorf("%w: provider %q base_url differs from the catalog default; confirm with --confirm-base-url", ErrProviderResolve, c.Name)
	}
	return base, nil
}

// builtinProtocols selects the first catalog protocol with a wired adapter;
// the F1 test/eval seam keeps the route on openai-chat, and an auth transport
// that is not wired is refused instead of being sent unauthenticated.
func (r *providerResolver) builtinProtocols(provName string, p *registry.Provider) ([]registry.Protocol, error) {
	if r.overrideBase != "" || r.e2eBase != "" || hasProtocol(p.Protocols, registry.ProtocolOpenAIChat) {
		return []registry.Protocol{registry.ProtocolOpenAIChat}, nil
	}
	for _, want := range []registry.Protocol{
		registry.ProtocolOpenAIResponses, registry.ProtocolAnthropicMessages, registry.ProtocolGemini,
	} {
		if !hasProtocol(p.Protocols, want) {
			continue
		}
		if err := adapterGate(want); err != nil {
			return nil, fmt.Errorf("%w: provider %q: %w", ErrProviderResolve, provName, err)
		}
		if !runtimeAuth(p.Auth) {
			return nil, fmt.Errorf("%w: provider %q auth %q needs the F5-03 transport", ErrProviderResolve, provName, p.Auth)
		}
		return []registry.Protocol{want}, nil
	}
	return nil, fmt.Errorf("%w: provider %q speaks %v; the adapter is not wired in this build", ErrProviderResolve, provName, p.Protocols)
}

// resolveBuiltin resolves a catalog row to the wire protocol that has an
// adapter. The F1 test/eval seam (--base-url / DAMNBOT_E2E_BASE_URL) asserts
// an OpenAI-compatible chat endpoint and keeps the route on openai-chat, as
// F5-02 did; a catalog preset selects its own protocol otherwise.
func (r *providerResolver) resolveBuiltin(provName, modelName string, p *registry.Provider) (provider.Route, error) {
	protocols, err := r.builtinProtocols(provName, p)
	if err != nil {
		return provider.Route{}, err
	}
	base := p.BaseURL
	if r.overrideBase != "" {
		base = r.overrideBase
	}
	if r.e2eBase != "" {
		base = r.e2eBase
	}
	if base == "" {
		return provider.Route{}, fmt.Errorf("%w: provider %q has no default base_url; add a [[providers.custom]] entry or --base-url", ErrProviderResolve, provName)
	}
	key := r.overrideKey
	for _, env := range p.Env {
		if key != "" {
			break
		}
		if v := r.getenv(env); v != "" {
			key = v
			break
		}
	}
	route, err := r.routeFor(p.ID, modelName, base, key, nil, nil, p.Attribution, protocols)
	if err != nil {
		return provider.Route{}, err
	}
	route.CacheTTLMax = p.CacheTTLMax
	return route, nil
}

// runtimeAuth reports whether the credential transport is wired (F5-02/F5-04):
// api-key, none, and unknown (honest catalog gap) run; oauth, github-token,
// AWS IAM, Google ADC, and ACP wait for F5-03.
func runtimeAuth(a registry.Auth) bool {
	return slices.Contains([]registry.Auth{registry.AuthAPIKey, registry.AuthNone, registry.AuthUnknown}, a)
}

// routeFor builds the single wire route shared by catalog and custom rows.
func (r *providerResolver) routeFor(provName, modelName, base, key string, extra map[string]string, body map[string]any, attribution []string, protocols []registry.Protocol) (provider.Route, error) {
	family, err := provider.NormalizeBaseURL(base)
	if err != nil {
		return provider.Route{}, fmt.Errorf("%w: %w", ErrProviderResolve, err)
	}
	prices, err := provider.RoutePrices(r.table, base, modelName)
	if err != nil {
		return provider.Route{}, fmt.Errorf("pricing: %w", err)
	}
	return provider.Route{
		Provider: provName, Model: modelName, BaseURL: base, APIKey: key, Family: family,
		ContextWindow: int64(r.cfg.Loop.TurnMaxInputTok),
		Headers:       r.headers(extra, attribution), ExtraBody: body, Prices: prices,
		Protocols: protocols,
	}, nil
}

// routes resolves the primary reference and the configured fallback chain.
// Fallback refs stay in the same order and are also registered as routes so
// provider.Client.Do can switch to them (PROV-074).
func (r *providerResolver) routes(ctx context.Context, ref string) ([]provider.Route, []string, error) {
	primary, err := r.resolve(ctx, ref)
	if err != nil {
		return nil, nil, err
	}
	out := []provider.Route{primary}
	fallback := make([]string, 0, len(r.cfg.Provider.Fallback))
	for _, fb := range r.cfg.Provider.Fallback {
		route, err := r.resolve(ctx, fb)
		if err != nil {
			return nil, nil, fmt.Errorf("fallback %q: %w", fb, err)
		}
		out = append(out, route)
		fallback = append(fallback, fb)
	}
	return out, fallback, nil
}

// providerDoctorWarnings computes the SEC-24 findings for DOC-009: every
// override, every non-default base_url, and every custom model without a
// route price. Sorted so doctor --json is stable.
func providerDoctorWarnings(cfg *config.Config, catalog *registry.Catalog, table registry.PriceTable) []string {
	var out []string
	if err := validateCustomOverrides(cfg, catalog); err != nil {
		out = append(out, err.Error())
	}
	for i := range cfg.Providers.Custom {
		c := &cfg.Providers.Custom[i]
		builtin, ok := catalog.Lookup(c.Name)
		base := c.BaseURL
		if base == "" && ok {
			base = builtin.BaseURL
		}
		if ok && c.Override {
			out = append(out, fmt.Sprintf("override %q replaces the built-in provider", c.Name))
			if c.BaseURL != "" && !sameBaseURL(c.BaseURL, builtin.BaseURL) {
				out = append(out, fmt.Sprintf("override %q uses non-default base_url %s", c.Name, c.BaseURL))
			}
		}
		for j := range c.Models {
			if m := &c.Models[j]; len(table.Lookup(base, m.ID)) == 0 {
				out = append(out, fmt.Sprintf("custom provider %q model %q is unpriced; monetary caps fail closed", c.Name, m.ID))
			}
		}
	}
	slices.Sort(out)
	return out
}

// base resolves only the endpoint identity (no credential requirement) for
// callers such as the model-limit override lookup.
func (r *providerResolver) base(ctx context.Context, ref string) (string, error) {
	route, err := r.resolve(ctx, ref)
	if err != nil {
		return "", err
	}
	return route.BaseURL, nil
}

// userAgent is the SEC-25 User-Agent: "damnbot/<versi>" without OS or host.
func userAgent() string { return "damnbot/" + version }

// validateCustomOverrides applies SEC-24 to every entry, not only the one a
// run references: a shadowing id without override=true fails at startup.
func validateCustomOverrides(cfg *config.Config, catalog *registry.Catalog) error {
	for i := range cfg.Providers.Custom {
		c := &cfg.Providers.Custom[i]
		if _, ok := catalog.Lookup(c.Name); ok && !c.Override {
			return fmt.Errorf("%w: [[providers.custom]] %q shadows a built-in provider; set override = true", ErrProviderResolve, c.Name)
		}
	}
	return nil
}

func (r *providerResolver) customProvider(name string) *config.CustomProvider {
	for i := range r.cfg.Providers.Custom {
		if strings.EqualFold(r.cfg.Providers.Custom[i].Name, name) {
			return &r.cfg.Providers.Custom[i]
		}
	}
	return nil
}

// keyFor returns the credential in precedence order: explicit --api-key,
// api_key_env, then the built-in catalog env list for an override.
func (r *providerResolver) keyFor(c *config.CustomProvider, builtin *registry.Provider, builtinOK bool) (string, error) {
	if r.overrideKey != "" {
		return r.overrideKey, nil
	}
	if hasCredentials(c) {
		// The pool supplies the per-attempt key; never fall through to the
		// built-in env list or a static key (config validation already
		// rejects a combination).
		return "", nil
	}
	if c.APIKeyEnv != "" {
		if v := r.getenv(c.APIKeyEnv); v != "" {
			return v, nil
		}
	}
	if c.APIKey != "" {
		return "", fmt.Errorf("%w: providers.custom[%q].api_key is a keychain reference; use api_key_env until F5-03 auth lands", ErrProviderResolve, c.Name)
	}
	if len(c.KeyCmd) > 0 {
		return "", fmt.Errorf("%w: providers.custom[%q].key_cmd needs the F5-03 auth transport", ErrProviderResolve, c.Name)
	}
	if builtinOK {
		for _, env := range builtin.Env {
			if v := r.getenv(env); v != "" {
				return v, nil
			}
		}
	}
	return "", nil
}

// checkCustomFields refuses fields the openai-chat adapter cannot honor
// rather than ignoring them (a silent no-op would break the operator's
// intent, e.g. streaming=false).
func checkCustomFields(c *config.CustomProvider) error {
	switch {
	case c.Streaming != nil && !*c.Streaming:
		return fmt.Errorf("%w: providers.custom[%q].streaming = false is not supported by the openai-chat adapter", ErrProviderResolve, c.Name)
	case c.TLS != nil:
		return fmt.Errorf("%w: providers.custom[%q].tls is not wired to netx yet", ErrProviderResolve, c.Name)
	case c.SessionAffinityHeader != "":
		return fmt.Errorf("%w: providers.custom[%q].session_affinity_header is not wired yet", ErrProviderResolve, c.Name)
	}
	return nil
}

// headers merges the opt-in attribution headers and the operator's
// extra_headers. With send_attribution=false (default) no attribution
// header is present (SEC-25).
func (r *providerResolver) headers(extra map[string]string, attribution []string) map[string]string {
	out := make(map[string]string, len(extra)+len(attribution))
	if r.attrib {
		for _, name := range attribution {
			out[name] = attributionValue(name)
		}
	}
	maps.Copy(out, extra)
	if len(out) == 0 {
		return nil
	}
	return out
}

func attributionValue(name string) string {
	if strings.EqualFold(name, "HTTP-Referer") {
		return attributionReferer
	}
	return attributionTitle
}

func sameBaseURL(a, b string) bool {
	return strings.EqualFold(strings.TrimRight(a, "/"), strings.TrimRight(b, "/"))
}

func hasProtocol(ps []registry.Protocol, want registry.Protocol) bool {
	return slices.Contains(ps, want)
}

func slicesContainsModel(models []config.CustomModel, id string) bool {
	return slices.ContainsFunc(models, func(m config.CustomModel) bool { return m.ID == id })
}

// lightRoute registers provider.light as an auxiliary route and returns its
// reference. It is never used for the primary turn; compaction is the first
// auxiliary consumer (SPEC §4.1). A ref equal to the primary is ignored.
func lightRoute(ctx context.Context, cfg *config.Config, modelRef string, routes *[]provider.Route, resolver *providerResolver, fallback []string) string {
	ref := cfg.Provider.Light
	if ref == "" || ref == modelRef || slices.Contains(fallback, ref) {
		return ""
	}
	route, err := resolver.resolve(ctx, ref)
	if err != nil {
		slog.WarnContext(ctx, "provider.light is not resolvable; compaction keeps the primary model", "ref", ref, "error", err)
		return ""
	}
	*routes = append(*routes, route)
	return ref
}

// routeModel returns the provider/model an auxiliary purpose runs on. Only
// compaction is routed to provider.light; the main turn always keeps its ref.
func (p *providerAdapter) routeModel(purpose provider.Purpose, model string) (string, string) {
	if purpose == provider.PurposeCompaction && p.lightRef != "" {
		if prov, _, ok := strings.Cut(p.lightRef, "/"); ok {
			return prov, p.lightRef
		}
	}
	prov, _, _ := strings.Cut(p.model, "/")
	return prov, model
}
