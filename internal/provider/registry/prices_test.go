package registry

import (
	"errors"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

const (
	teamRouterBase    = "https://api.teamorouter.com/v1"
	teamRouterModel   = "gpt-5.4-mini"
	teamRouterVersion = "providers-2026-10-21/teamorouter-gpt-5.4-mini"
)

func defaultTable(t *testing.T) PriceTable {
	t.Helper()
	table, err := DefaultPriceTable()
	if err != nil {
		t.Fatalf("default price table: %v", err)
	}
	return table
}

// TestDefaultPriceTableTeamRouter: the operator-contracted TeamRouter entry
// is found by the actual base_url + model and carries its cited source and
// authority.
func TestDefaultPriceTableTeamRouter(t *testing.T) {
	t.Parallel()
	got := defaultTable(t).Lookup(teamRouterBase, teamRouterModel)
	if len(got) != 1 {
		t.Fatalf("lookup returned %d entries, want 1", len(got))
	}
	p := got[0]
	if p.InputPerMTok != 150_000 || p.CachedInputPerMTok != 15_000 || p.OutputPerMTok != 600_000 {
		t.Errorf("prices = %d/%d/%d, want 150000/15000/600000", p.InputPerMTok, p.CachedInputPerMTok, p.OutputPerMTok)
	}
	if p.Kind != PriceOperator || !strings.Contains(p.Source, "owner decision 2026-10-21") {
		t.Errorf("kind %q source %q, want operator-contracted with the owner decision", p.Kind, p.Source)
	}
	if p.Version != teamRouterVersion {
		t.Errorf("version = %q, want %q", p.Version, teamRouterVersion)
	}
	if p.BaseURL != teamRouterBase || p.Model != teamRouterModel {
		t.Errorf("entry route %q/%q, want %q/%q", p.BaseURL, p.Model, teamRouterBase, teamRouterModel)
	}
	if p.From.IsZero() || !p.Until.IsZero() {
		t.Errorf("from %s until %s, want the effective date and no expiry", p.From, p.Until)
	}
}

// TestPriceAliases: the same route spelled differently is the same route.
func TestPriceAliases(t *testing.T) {
	t.Parallel()
	table := defaultTable(t)
	for _, base := range []string{
		"HTTPS://API.TEAMOROUTER.COM/v1/",
		"https://api.teamorouter.com:443/v1/",
		teamRouterBase,
	} {
		if alias := table.Lookup(base, teamRouterModel); len(alias) != 1 || alias[0].Version != teamRouterVersion {
			t.Errorf("lookup %q = %v, want the TeamRouter entry", base, alias)
		}
	}
}

// TestPriceNotInherited: another route with the same model never inherits
// the price (fail closed), and the zero table prices nothing.
func TestPriceNotInherited(t *testing.T) {
	t.Parallel()
	table := defaultTable(t)
	for _, route := range [][2]string{
		{"https://api.openai.com/v1", teamRouterModel},
		{"https://api.teamorouter.com/v2", teamRouterModel},
		{teamRouterBase, "gpt-5.4"},
		{"https://other-reseller.example/v1", teamRouterModel},
	} {
		if got := table.Lookup(route[0], route[1]); got != nil {
			t.Errorf("lookup %s/%s = %+v, want unpriced", route[0], route[1], got)
		}
	}
	if got := (PriceTable{}).Lookup(teamRouterBase, teamRouterModel); got != nil {
		t.Errorf("zero table prices %+v", got)
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	t.Parallel()
	ok := map[string]string{
		"https://api.example.com/v1":       "https://api.example.com/v1",
		"HTTPS://API.Example.COM/V1":       "https://api.example.com/V1", // path is case-sensitive
		"https://api.example.com:443/v1//": "https://api.example.com/v1",
		"http://api.example.com:80":        "http://api.example.com",
		"https://[::1]:8443/v1":            "https://[::1]:8443/v1",
		"https://api.example.com/v1/%20x":  "https://api.example.com/v1/%20x",
	}
	for in, want := range ok {
		if got, err := NormalizeBaseURL(in); err != nil || got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"api.example.com/v1",
		"ftp://api.example.com/v1",
		"https://api.example.com/v1?key=x",
		"https://api.example.com/v1#frag",
		"https://user:pass@api.example.com/v1",
		"https://",
	} {
		if got, err := NormalizeBaseURL(bad); !errors.Is(err, ErrPrice) {
			t.Errorf("NormalizeBaseURL(%q) = %q, %v; want ErrPrice", bad, got, err)
		}
	}
}

// validEntry is a well-formed vendor price used as the mutation base.
func validEntry() priceEntry {
	return priceEntry{
		ID: "vendor-m", BaseURL: "https://api.example.com/v1", Model: "m",
		Kind: "vendor", Source: "vendor list price, 2026-01-01", From: "2026-01-01",
		Input: 100, CachedInput: 10, Output: 200,
	}
}

func encode(t *testing.T, f priceFile) string {
	t.Helper()
	var b strings.Builder
	if err := toml.NewEncoder(&b).Encode(f); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestParsePriceTableRejectsInvalid: a malformed or unsafe table fails at
// load, never as a silently zero or inherited price at call time.
func TestParsePriceTableRejectsInvalid(t *testing.T) {
	t.Parallel()
	valid := encode(t, priceFile{Version: "t", Price: []priceEntry{validEntry()}})
	if table, err := ParsePriceTable([]byte(valid)); err != nil {
		t.Fatalf("valid table rejected: %v", err)
	} else if got := table.Lookup("https://api.example.com/v1", "m"); len(got) != 1 ||
		got[0].Kind != PriceVendor || got[0].Version != "t/vendor-m" {
		t.Errorf("valid table lookup = %+v", got)
	}

	mutate := func(f func(*priceEntry)) string {
		e := validEntry()
		f(&e)
		return encode(t, priceFile{Version: "t", Price: []priceEntry{e}})
	}
	second := func(f func(*priceEntry)) string {
		e := validEntry()
		f(&e)
		return encode(t, priceFile{Version: "t", Price: []priceEntry{validEntry(), e}})
	}
	cases := []struct {
		name string
		data string
	}{
		{"malformed TOML", "version = \"t\"\n[[price]\nid = \"x\"\n"},
		{"unknown key", strings.Replace(valid, "input = 100", "imput = 100", 1)},
		{"missing version", encode(t, priceFile{Price: []priceEntry{validEntry()}})},
		{"missing id", mutate(func(e *priceEntry) { e.ID = "" })},
		{"missing model", mutate(func(e *priceEntry) { e.Model = "" })},
		{"missing source", mutate(func(e *priceEntry) { e.Source = "  " })},
		{"unknown kind", mutate(func(e *priceEntry) { e.Kind = "guess" })},
		{"missing from", mutate(func(e *priceEntry) { e.From = "" })},
		{"bad until", mutate(func(e *priceEntry) { e.Until = "2026-13-01" })},
		{"until equals from", mutate(func(e *priceEntry) { e.Until = e.From })},
		{"negative price", mutate(func(e *priceEntry) { e.Output = -1 })},
		{"base_url query", mutate(func(e *priceEntry) { e.BaseURL = "https://api.example.com/v1?k=v" })},
		{"duplicate id", second(func(e *priceEntry) { e.BaseURL = "https://api.other.example/v1" })},
		{"overlap", second(func(e *priceEntry) { e.ID = "b"; e.From = "2026-02-01" })},
	}
	for _, c := range cases {
		if _, err := ParsePriceTable([]byte(c.data)); !errors.Is(err, ErrPrice) {
			t.Errorf("%s: err = %v, want ErrPrice", c.name, err)
		}
	}

	// Adjacent ranges (until == next from) are a valid schedule.
	two := validEntry()
	two.ID, two.From, two.Input = "b", "2026-06-01", 300
	first := validEntry()
	first.Until = "2026-06-01"
	if _, err := ParsePriceTable([]byte(encode(t, priceFile{Version: "t", Price: []priceEntry{two, first}}))); err != nil {
		t.Errorf("adjacent dated entries rejected: %v", err)
	}
}
