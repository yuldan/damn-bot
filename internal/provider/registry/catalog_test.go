package registry

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// defaultCatalogForTest parses the embedded catalog once per test.
func defaultCatalogForTest(t *testing.T) *Catalog {
	t.Helper()
	c, err := DefaultCatalog()
	if err != nil {
		t.Fatalf("default catalog: %v", err)
	}
	return c
}

// TestProviderCatalog88 is the F5-01 count contract: 88 PROVIDERS.md rows plus
// six local servers, each row parsed and validated from the embedded TOML.
func TestProviderCatalog88(t *testing.T) {
	t.Parallel()
	c := defaultCatalogForTest(t)
	if got := c.ProviderCount(); got != 88 {
		t.Fatalf("provider rows = %d, want 88", got)
	}
	locals := 0
	for _, p := range c.Providers() {
		if p.Local {
			locals++
		}
	}
	if locals != 6 || c.Len() != 94 {
		t.Fatalf("local = %d, total = %d; want 6 and 94", locals, c.Len())
	}
	checkCatalogAnchors(t, c)
}

// checkCatalogAnchors spot-checks the facts F5-02 relies on: lookup by id,
// by alias, and the protocol that selects an adapter.
func checkCatalogAnchors(t *testing.T, c *Catalog) {
	t.Helper()
	anthropic, ok := c.Lookup("anthropic")
	if !ok || !slices.Equal(anthropic.Protocols, []Protocol{ProtocolAnthropicMessages}) {
		t.Fatalf("anthropic = %+v, %v", anthropic, ok)
	}
	if p, ok := c.Lookup("claude"); !ok || p.ID != "anthropic" {
		t.Fatalf("alias claude = %q, %v; want anthropic", p.ID, ok)
	}
	if p, ok := c.Lookup("llama.cpp"); !ok || p.ID != "custom" {
		t.Fatalf("alias llama.cpp = %q, %v; want custom", p.ID, ok)
	}
	if p, ok := c.Lookup("grok"); !ok || p.Protocols[0] != ProtocolOpenAIResponses {
		t.Fatalf("alias grok = %+v, %v; want xai openai-responses", p, ok)
	}
	if p, ok := c.Lookup("local-apple"); !ok || !p.Local || p.Protocols[0] != ProtocolApple {
		t.Fatalf("local-apple = %+v, %v", p, ok)
	}
}

// TestProviderAliasUnique: ids and aliases share one namespace; every name
// resolves to exactly one row and an alias never shadows another id.
func TestProviderAliasUnique(t *testing.T) {
	t.Parallel()
	c := defaultCatalogForTest(t)
	owner := map[string]string{}
	for _, p := range c.Providers() {
		for _, name := range append([]string{p.ID}, p.Aliases...) {
			key := strings.ToLower(name)
			if prev, ok := owner[key]; ok {
				t.Fatalf("name %q is claimed by %q and %q", name, prev, p.ID)
			}
			owner[key] = p.ID
			got, ok := c.Lookup(strings.ToUpper(name))
			if !ok || got.ID != p.ID {
				t.Fatalf("Lookup(%q) = %q, %v; want %q", name, got.ID, ok, p.ID)
			}
		}
	}
	if _, ok := c.Lookup(""); ok {
		t.Fatal("empty name resolved")
	}
	if _, ok := c.Lookup("no-such-provider"); ok {
		t.Fatal("unknown name resolved")
	}
}

// TestCatalogRejects pins the loader gates: a duplicate alias, a duplicate
// protocol, and an unknown enum are startup errors, never a silent default.
func TestCatalogRejects(t *testing.T) {
	t.Parallel()
	const entry = `[[provider]]
id = "a"
name = "A"
protocols = ["openai-chat"]
auth = "api-key"
aliases = ["one"]
`
	cases := map[string]string{
		"duplicate alias": entry + `
[[provider]]
id = "b"
name = "B"
protocols = ["openai-chat"]
auth = "api-key"
aliases = ["ONE"]
`,
		"duplicate protocol": `[[provider]]
id = "a"
name = "A"
protocols = ["openai-chat", "openai-chat"]
auth = "api-key"
`,
		"unknown protocol": `[[provider]]
id = "a"
name = "A"
protocols = ["grpc"]
auth = "api-key"
`,
		"unknown auth": `[[provider]]
id = "a"
name = "A"
protocols = ["openai-chat"]
auth = "token"
`,
		"unknown capability": `[[provider]]
id = "a"
name = "A"
protocols = ["openai-chat"]
auth = "none"
capabilities = ["audio"]
`,
		"unknown prices": `[[provider]]
id = "a"
name = "A"
protocols = ["openai-chat"]
auth = "none"
prices = "cheap"
`,
		"unknown key": `[[provider]]
id = "a"
name = "A"
protocols = ["openai-chat"]
auth = "none"
bogus = true
`,
		"bad base url": `[[provider]]
id = "a"
name = "A"
protocols = ["openai-chat"]
auth = "none"
base_url = "ftp://example.com"
`,
		"negative cache": `[[provider]]
id = "a"
name = "A"
protocols = ["anthropic-messages"]
auth = "none"
cache_ttl_max = -1
`,
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseCatalog([]byte("version = \"test\"\n" + data)); err == nil {
				t.Fatalf("ParseCatalog accepted %s", name)
			}
		})
	}
}

// TestCatalogRejectsBadEnv pins the credential-name grammar: env is a
// variable name, never a value or a shell expression.
func TestCatalogRejectsBadEnv(t *testing.T) {
	t.Parallel()
	data := `version = "test"
[[provider]]
id = "a"
name = "A"
protocols = ["openai-chat"]
auth = "api-key"
env = ["lower_case"]
`
	if _, err := ParseCatalog([]byte(data)); err == nil {
		t.Fatal("ParseCatalog accepted a lowercase env name")
	}
}

// TestProvidersMDParity: every PROVIDERS.md row has a catalog entry with the
// same display name and protocol set, and the catalog has no extra row.
// PROVIDERS.md is the owner of the 88-row list; the TOML is the executable
// copy (SPEC §4.1).
func TestProvidersMDParity(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../../../docs/PROVIDERS.md")
	if err != nil {
		t.Fatalf("read PROVIDERS.md: %v", err)
	}
	c := defaultCatalogForTest(t)
	byName := map[string]Provider{}
	for _, p := range c.Providers() {
		if !p.Local {
			byName[p.Name] = p
		}
	}
	seen := 0
	for line := range strings.SplitSeq(string(data), "\n") {
		if !strings.HasPrefix(line, "| LLM-") {
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) < 3 {
			t.Fatalf("malformed PROVIDERS.md row %q", line)
		}
		seen++
		name := docName(strings.TrimSpace(cells[1]))
		want := docProtocols(strings.TrimSpace(cells[2]))
		p, ok := byName[name]
		if !ok {
			t.Errorf("PROVIDERS.md %s %q: no catalog entry", strings.TrimSpace(cells[0]), name)
			continue
		}
		if got := sortedProtocols(p.Protocols); !slices.Equal(got, want) {
			t.Errorf("PROVIDERS.md %s %q: catalog protocols %v, doc %v", strings.TrimSpace(cells[0]), name, got, want)
		}
	}
	if seen != 88 {
		t.Fatalf("PROVIDERS.md rows = %d, want 88", seen)
	}
	if len(byName) != 88 {
		t.Fatalf("catalog non-local names = %d, want 88", len(byName))
	}
}

// docName extracts "Display Name" from `Display Name (\`id\`)` or "\`id\`".
func docName(cell string) string {
	if strings.HasPrefix(cell, "`") && strings.HasSuffix(cell, "`") {
		return strings.Trim(cell, "`")
	}
	if i := strings.Index(cell, " (`"); i > 0 {
		return strings.TrimSpace(cell[:i])
	}
	return cell
}

// docProtocols translates the PROVIDERS.md protocol column to catalog values.
func docProtocols(cell string) []Protocol {
	var out []Protocol
	for part := range strings.SplitSeq(cell, ",") {
		switch strings.TrimSpace(part) {
		case "openai-chat", "openai-chat?":
			out = append(out, ProtocolOpenAIChat)
		case "openai-responses":
			out = append(out, ProtocolOpenAIResponses)
		case "anthropic-messages":
			out = append(out, ProtocolAnthropicMessages)
		case "gemini":
			out = append(out, ProtocolGemini)
		case "bedrock":
			out = append(out, ProtocolBedrock)
		case "lainnya (cursor)":
			out = append(out, ProtocolCursor)
		case "lainnya (factory)":
			out = append(out, ProtocolFactory)
		}
	}
	return sortedProtocols(out)
}

func sortedProtocols(in []Protocol) []Protocol {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}
