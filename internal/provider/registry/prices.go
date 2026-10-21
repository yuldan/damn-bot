package registry

import (
	_ "embed"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// PriceKind is who published a price entry: the model vendor's list price or
// the operator contract of this route (reseller). The kind is descriptive
// provenance; the lookup key alone (base_url + model) decides what applies.
type PriceKind string

// Price kinds. An operator-contracted price is route-specific by definition
// and never applies to another base_url.
const (
	PriceVendor   PriceKind = "vendor"
	PriceOperator PriceKind = "operator-contracted"
)

// Price is one dated price of the route (base_url, model), in micro-USD per
// million tokens (the budget.Price convention). It applies on the half-open
// interval [From, Until) in UTC; a zero Until is open-ended.
type Price struct {
	BaseURL, Model     string
	Kind               PriceKind
	Source             string
	InputPerMTok       int64
	CachedInputPerMTok int64 // 0 = not discounted
	OutputPerMTok      int64
	From, Until        time.Time
	Version            string // <table>/<id>, recorded in usage_ledger
}

// PriceTable is the validated, immutable pricing registry of providers.toml.
// The zero value prices nothing: every route is unpriced and a monetary cap
// fails closed (ADR-123). Safe to share.
type PriceTable struct{ routes map[string][]Price }

// ErrPrice rejects an invalid price registry or base_url. The composition
// root must fail at startup: a typo must not silently turn a route into
// "unpriced" or a price into zero.
var ErrPrice = errors.New("registry: invalid pricing")

//go:embed providers.toml
var providersTOML []byte

// DefaultPriceTable parses the embedded providers.toml.
func DefaultPriceTable() (PriceTable, error) { return ParsePriceTable(providersTOML) }

// priceFile and priceEntry are the TOML shape; dates are strings so a typo
// is a parse error rather than a silently zero time.
type priceFile struct {
	Version string       `toml:"version"`
	Price   []priceEntry `toml:"price"`
}

type priceEntry struct {
	ID          string `toml:"id"`
	BaseURL     string `toml:"base_url"`
	Model       string `toml:"model"`
	Kind        string `toml:"kind"`
	Source      string `toml:"source"`
	From        string `toml:"from"`
	Until       string `toml:"until"`
	Input       int64  `toml:"input"`
	CachedInput int64  `toml:"cached_input"`
	Output      int64  `toml:"output"`
}

// ParsePriceTable decodes and validates a providers.toml. It rejects unknown
// keys, a missing table version or source, an unknown kind, a missing or
// malformed date, a negative price, a duplicate entry id, and overlapping
// ranges for one route: two prices at one instant would make the bill
// ambiguous, so the schedule is refused rather than resolved.
func ParsePriceTable(data []byte) (PriceTable, error) {
	var f priceFile
	md, err := toml.Decode(string(data), &f)
	if err != nil {
		return PriceTable{}, fmt.Errorf("%w: %w", ErrPrice, err)
	}
	if und := md.Undecoded(); len(und) > 0 {
		return PriceTable{}, fmt.Errorf("%w: unknown key %q", ErrPrice, und[0].String())
	}
	table := strings.TrimSpace(f.Version)
	if table == "" {
		return PriceTable{}, fmt.Errorf("%w: missing table version", ErrPrice)
	}
	t := PriceTable{routes: make(map[string][]Price, len(f.Price))}
	seen := make(map[string]bool, len(f.Price))
	for i := range f.Price {
		p, key, err := f.Price[i].parse(table)
		if err != nil {
			return PriceTable{}, fmt.Errorf("%w: entry %d (%q): %w", ErrPrice, i+1, f.Price[i].ID, err)
		}
		if seen[p.Version] {
			return PriceTable{}, fmt.Errorf("%w: duplicate entry id %q", ErrPrice, f.Price[i].ID)
		}
		seen[p.Version] = true
		t.routes[key] = append(t.routes[key], p)
	}
	for key, ps := range t.routes {
		slices.SortStableFunc(ps, func(a, b Price) int { return a.From.Compare(b.From) })
		for i := 1; i < len(ps); i++ {
			if prev := ps[i-1]; prev.Until.IsZero() || prev.Until.After(ps[i].From) {
				return PriceTable{}, fmt.Errorf("%w: %q overlaps %q", ErrPrice, ps[i].Version, prev.Version)
			}
		}
		t.routes[key] = ps
	}
	return t, nil
}

// parse validates one entry and returns it with its lookup key.
func (e *priceEntry) parse(table string) (Price, string, error) {
	if strings.TrimSpace(e.ID) == "" {
		return Price{}, "", errors.New("missing id")
	}
	key, err := NormalizeBaseURL(e.BaseURL)
	if err != nil {
		return Price{}, "", err
	}
	if e.Model == "" {
		return Price{}, "", errors.New("missing model")
	}
	kind := PriceKind(strings.TrimSpace(e.Kind))
	if kind != PriceVendor && kind != PriceOperator {
		return Price{}, "", fmt.Errorf("kind %q is not %q or %q", e.Kind, PriceVendor, PriceOperator)
	}
	src := strings.TrimSpace(e.Source)
	if src == "" {
		return Price{}, "", errors.New("missing source")
	}
	from, err := parseWhen(e.From)
	if err != nil {
		return Price{}, "", fmt.Errorf("from: %w", err)
	}
	var until time.Time
	if e.Until != "" {
		if until, err = parseWhen(e.Until); err != nil {
			return Price{}, "", fmt.Errorf("until: %w", err)
		}
		if !until.After(from) {
			return Price{}, "", fmt.Errorf("until %s is not after from %s", e.Until, e.From)
		}
	}
	if e.Input < 0 || e.CachedInput < 0 || e.Output < 0 {
		return Price{}, "", errors.New("negative price")
	}
	return Price{
		BaseURL: key, Model: e.Model, Kind: kind, Source: src,
		InputPerMTok: e.Input, CachedInputPerMTok: e.CachedInput, OutputPerMTok: e.Output,
		From: from, Until: until, Version: table + "/" + e.ID,
	}, routeKey(key, e.Model), nil
}

// Lookup returns the dated entries of the exact route (normalized base_url,
// model), ordered by From, or nil when the table does not price it. Another
// base_url with the same model never inherits the price. The result is
// read-only.
func (t PriceTable) Lookup(baseURL, model string) []Price {
	key, err := NormalizeBaseURL(baseURL)
	if err != nil || model == "" {
		return nil
	}
	return t.routes[routeKey(key, model)]
}

func routeKey(baseURL, model string) string { return baseURL + "\x00" + model }

// parseWhen accepts a date (UTC midnight) or an RFC 3339 instant.
func parseWhen(s string) (time.Time, error) {
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("date %q is not YYYY-MM-DD or RFC3339", s)
	}
	return t.UTC(), nil
}

// NormalizeBaseURL canonicalizes a route base_url for the price lookup:
// lowercased scheme and host, default port dropped, trailing slashes
// removed. The escaped path is compared exactly and query/fragment/userinfo
// are refused, so an unusual URL can never silently match another route's
// price (it stays unpriced, the fail-closed direction).
func NormalizeBaseURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: base_url %q: %w", ErrPrice, raw, err)
	}
	scheme, host, err := plainOrigin(u, raw)
	if err != nil {
		return "", err
	}
	return scheme + "://" + host + strings.TrimRight(u.EscapedPath(), "/"), nil
}

// plainOrigin returns the lowercased scheme and canonical host:port of a
// plain http(s) URL without userinfo, query, or fragment.
func plainOrigin(u *url.URL, raw string) (scheme, host string, err error) {
	scheme = strings.ToLower(u.Scheme)
	if (scheme != "http" && scheme != "https") || u.Host == "" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", "", fmt.Errorf("%w: base_url %q is not a plain http(s) origin+path", ErrPrice, raw)
	}
	host = strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") && port == "" { // IPv6 literal
		host = "[" + host + "]"
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	}
	return scheme, host, nil
}
