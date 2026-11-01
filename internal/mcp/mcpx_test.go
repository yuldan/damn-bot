package mcp

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// mcpxEntry is one optional MCP from the F5 MCPX catalog (SPEC §9.6; the
// manifest table is docs/FEATURE-PARITY.md). The test below is the acceptance
// gate for "65/65 initialize + tools/list": every entry is driven through the
// real client state machine against a fake server.
type mcpxEntry struct {
	ID          string
	Name        string
	Description string
	URL         string
	Auth        string
}

// mcpxCatalog mirrors docs/FEATURE-PARITY.md rows MCPX-001..MCPX-065.
func mcpxCatalog() []mcpxEntry {
	return []mcpxEntry{
		{ID: "MCPX-001", Name: "airtable", Description: "Bases, tables, and records from your Airtable workspace.", URL: "https://mcp.airtable.com/mcp", Auth: "oauth"},
		{ID: "MCPX-002", Name: "algolia", Description: "Algolia search: indices, analytics, and settings (read-only).", URL: "https://mcp.algolia.com/mcp", Auth: "oauth"},
		{ID: "MCPX-003", Name: "alltrails", Description: "AllTrails: find hikes and trails with reviews and ratings.", URL: "https://www.alltrails.com/mcp", Auth: "none"},
		{ID: "MCPX-004", Name: "amplitude", Description: "Amplitude analytics: charts, dashboards, experiments, flags.", URL: "https://mcp.amplitude.com/mcp", Auth: "oauth"},
		{ID: "MCPX-005", Name: "asana", Description: "Tasks, projects, and goals from your Asana workspace.", URL: "https://mcp.asana.com/v2/mcp", Auth: "oauth"},
		{ID: "MCPX-006", Name: "atlassian", Description: "Jira issues and Confluence pages via Atlassian's hosted remote MCP.", URL: "https://mcp.atlassian.com/v1/mcp/authv2", Auth: "oauth"},
		{ID: "MCPX-007", Name: "attio", Description: "CRM records, lists, and notes in Attio.", URL: "https://mcp.attio.com/mcp", Auth: "oauth"},
		{ID: "MCPX-008", Name: "aws-knowledge", Description: "Authoritative AWS docs, API references, and best practices.", URL: "https://knowledge-mcp.global.api.aws", Auth: "none"},
		{ID: "MCPX-009", Name: "betterstack", Description: "Better Stack: logs, uptime monitors, incidents, and status pages.", URL: "https://mcp.betterstack.com", Auth: "oauth"},
		{ID: "MCPX-010", Name: "buildkite", Description: "CI/CD pipelines, builds, and test results from Buildkite.", URL: "https://mcp.buildkite.com/mcp", Auth: "oauth"},
		{ID: "MCPX-011", Name: "calendly", Description: "Scheduling links, events, and invitees from Calendly.", URL: "https://mcp.calendly.com", Auth: "oauth"},
		{ID: "MCPX-012", Name: "canva", Description: "Create, search, and manage Canva designs.", URL: "https://mcp.canva.com/mcp", Auth: "oauth"},
		{ID: "MCPX-013", Name: "circleci", Description: "CircleCI: diagnose build failures, read logs, rerun workflows.", URL: "https://mcp.circleci.com/v1/mcp", Auth: "oauth"},
		{ID: "MCPX-014", Name: "clickup", Description: "Tasks, docs, and workspaces in ClickUp.", URL: "https://mcp.clickup.com/mcp", Auth: "oauth"},
		{ID: "MCPX-015", Name: "close", Description: "Sales CRM: leads, opportunities, calls, and emails.", URL: "https://mcp.close.com/mcp", Auth: "oauth"},
		{ID: "MCPX-016", Name: "cloudflare", Description: "Full Cloudflare API access via the official remote MCP.", URL: "https://mcp.cloudflare.com/mcp?codemode=false", Auth: "oauth"},
		{ID: "MCPX-017", Name: "cloudinary", Description: "Upload, search, and transform media assets in Cloudinary.", URL: "https://asset-management.mcp.cloudinary.com/mcp", Auth: "oauth"},
		{ID: "MCPX-018", Name: "comfy-cloud", Description: "Generate images, video, audio, and 3D on Comfy Cloud.", URL: "https://cloud.comfy.org/mcp", Auth: "oauth"},
		{ID: "MCPX-019", Name: "context7", Description: "Up-to-date, version-specific library docs and code examples.", URL: "https://mcp.context7.com/mcp", Auth: "none"},
		{ID: "MCPX-020", Name: "craft", Description: "Craft: structured docs, tasks, and personal knowledge base.", URL: "https://mcp.craft.do/my/mcp", Auth: "oauth"},
		{ID: "MCPX-021", Name: "datadog", Description: "Logs, monitors, dashboards, and incidents from Datadog.", URL: "https://mcp.datadoghq.com/api/unstable/mcp-server/mcp", Auth: "oauth"},
		{ID: "MCPX-022", Name: "deepwiki", Description: "Ask questions about any public GitHub repo (Devin's DeepWiki).", URL: "https://mcp.deepwiki.com/mcp", Auth: "none"},
		{ID: "MCPX-023", Name: "dropbox", Description: "Search, read, and manage files in Dropbox.", URL: "https://mcp.dropbox.com/mcp", Auth: "oauth"},
		{ID: "MCPX-024", Name: "figma", Description: "Official Figma remote MCP — design context, Code Connect, and write-to-canvas via https://mcp.figma.com/mcp (OAuth).", URL: "https://mcp.figma.com/mcp", Auth: "oauth"},
		{ID: "MCPX-025", Name: "fireflies", Description: "Meeting transcripts, summaries, and action items.", URL: "https://api.fireflies.ai/mcp", Auth: "oauth"},
		{ID: "MCPX-026", Name: "gamma", Description: "Gamma: generate and edit AI presentations, docs, and sites.", URL: "https://mcp.gamma.app/mcp", Auth: "oauth"},
		{ID: "MCPX-027", Name: "gitlab", Description: "GitLab: issues, merge requests, pipelines, and repo context.", URL: "https://gitlab.com/api/v4/mcp", Auth: "oauth"},
		{ID: "MCPX-028", Name: "globalping", Description: "Ping, traceroute, DNS, and HTTP tests from global probes.", URL: "https://mcp.globalping.dev/mcp", Auth: "oauth"},
		{ID: "MCPX-029", Name: "grafana", Description: "Query metrics, logs, dashboards, alerts, and incidents from Grafana Cloud.", URL: "https://mcp.grafana.com/mcp", Auth: "oauth"},
		{ID: "MCPX-030", Name: "hugging_face", Description: "Models, datasets, Spaces, and papers from the Hugging Face Hub.", URL: "https://huggingface.co/mcp", Auth: "oauth"},
		{ID: "MCPX-031", Name: "indeed", Description: "Search jobs and listings on Indeed.", URL: "https://mcp.indeed.com/claude/mcp", Auth: "oauth"},
		{ID: "MCPX-032", Name: "intercom", Description: "Conversations, tickets, and customer data from Intercom.", URL: "https://mcp.intercom.com/mcp", Auth: "oauth"},
		{ID: "MCPX-033", Name: "kiwi", Description: "Kiwi.com flight search: itineraries with direct booking links.", URL: "https://mcp.kiwi.com", Auth: "none"},
		{ID: "MCPX-034", Name: "klaviyo", Description: "Klaviyo marketing: campaigns, flows, segments, and reporting.", URL: "https://mcp.klaviyo.com/mcp?core-tools-only=true&disable-tools-with-user-generated-content=true", Auth: "oauth"},
		{ID: "MCPX-035", Name: "linear", Description: "Find, create, and update Linear issues, projects, and comments.", URL: "https://mcp.linear.app/mcp", Auth: "oauth"},
		{ID: "MCPX-036", Name: "microsoft-learn", Description: "Official Microsoft, Azure, and .NET docs and code samples.", URL: "https://learn.microsoft.com/api/mcp", Auth: "none"},
		{ID: "MCPX-037", Name: "miro", Description: "Read and edit Miro boards, diagrams, and frames.", URL: "https://mcp.miro.com/", Auth: "oauth"},
		{ID: "MCPX-038", Name: "mixpanel", Description: "Mixpanel analytics: events, funnels, retention, dashboards.", URL: "https://mcp.mixpanel.com/mcp", Auth: "oauth"},
		{ID: "MCPX-039", Name: "monday", Description: "Boards, items, docs, and workflows in monday.com.", URL: "https://mcp.monday.com/mcp", Auth: "oauth"},
		{ID: "MCPX-040", Name: "motherduck", Description: "MotherDuck: query DuckDB cloud warehouses with SQL.", URL: "https://api.motherduck.com/mcp", Auth: "oauth"},
		{ID: "MCPX-041", Name: "n8n-official", Description: "Connect to your n8n instance's official MCP server with browser OAuth.", URL: "${N8N_MCP_SERVER_URL}", Auth: "oauth"},
		{ID: "MCPX-042", Name: "neon", Description: "Neon serverless Postgres: projects, branches, and SQL.", URL: "https://mcp.neon.tech/mcp", Auth: "oauth"},
		{ID: "MCPX-043", Name: "netlify", Description: "Sites, deploys, and env vars via Netlify's hosted MCP.", URL: "https://netlify-mcp.netlify.app/mcp", Auth: "oauth"},
		{ID: "MCPX-044", Name: "notion", Description: "Pages and databases from your Notion workspace.", URL: "https://mcp.notion.com/mcp", Auth: "oauth"},
		{ID: "MCPX-045", Name: "paypal", Description: "Payments, invoices, and subscriptions via PayPal's hosted MCP.", URL: "https://mcp.paypal.com/sse", Auth: "oauth"},
		{ID: "MCPX-046", Name: "plaid", Description: "Plaid dashboard: integrations, Items, and usage debugging.", URL: "https://api.dashboard.plaid.com/mcp/", Auth: "oauth"},
		{ID: "MCPX-047", Name: "postman", Description: "Postman workspaces, collections, environments, and APIs.", URL: "https://mcp.postman.com/minimal", Auth: "oauth"},
		{ID: "MCPX-048", Name: "prisma-postgres", Description: "Create and manage Prisma Postgres databases.", URL: "https://mcp.prisma.io/mcp", Auth: "oauth"},
		{ID: "MCPX-049", Name: "railway", Description: "Railway: projects, services, deployments, and environments.", URL: "https://mcp.railway.com", Auth: "oauth"},
		{ID: "MCPX-050", Name: "robinhood", Description: "Robinhood agentic trading: portfolio, balances, and orders.", URL: "https://agent.robinhood.com/mcp/trading", Auth: "oauth"},
		{ID: "MCPX-051", Name: "semgrep", Description: "Scan code for security vulnerabilities with Semgrep.", URL: "https://mcp.semgrep.ai/mcp", Auth: "oauth"},
		{ID: "MCPX-052", Name: "sentry", Description: "Issues, stack traces, and error context from Sentry.", URL: "https://mcp.sentry.dev/mcp", Auth: "oauth"},
		{ID: "MCPX-053", Name: "square", Description: "Catalog, orders, and payments via Square's hosted MCP.", URL: "https://mcp.squareup.com/sse", Auth: "oauth"},
		{ID: "MCPX-054", Name: "strava", Description: "Strava: activities, fitness trends, training load (read-only).", URL: "https://mcp.strava.com/mcp", Auth: "oauth"},
		{ID: "MCPX-055", Name: "stripe", Description: "Payments, customers, and invoices via Stripe's hosted MCP.", URL: "https://mcp.stripe.com", Auth: "oauth"},
		{ID: "MCPX-056", Name: "supabase", Description: "Database, auth, and storage from your Supabase projects.", URL: "https://mcp.supabase.com/mcp", Auth: "oauth"},
		{ID: "MCPX-057", Name: "todoist", Description: "Manage Todoist tasks and projects.", URL: "https://ai.todoist.net/mcp", Auth: "oauth"},
		{ID: "MCPX-058", Name: "trivago", Description: "trivago hotel search: compare prices by city and dates.", URL: "https://mcp.trivago.com/mcp", Auth: "none"},
		{ID: "MCPX-059", Name: "twelve-data", Description: "Stocks, forex, and crypto market data from Twelve Data.", URL: "https://mcp.twelvedata.com/mcp", Auth: "oauth"},
		{ID: "MCPX-060", Name: "twilio-docs", Description: "Twilio developer docs search (public beta, read-only).", URL: "https://mcp.twilio.com/docs", Auth: "none"},
		{ID: "MCPX-061", Name: "unreal-engine", Description: "Drive the Unreal Engine 5.8 editor over its local MCP server.", URL: "http://127.0.0.1:8000/mcp", Auth: "none"},
		{ID: "MCPX-062", Name: "vercel", Description: "Deployments, logs, and projects via Vercel's hosted MCP.", URL: "https://mcp.vercel.com", Auth: "oauth"},
		{ID: "MCPX-063", Name: "webflow", Description: "Sites, CMS collections, and pages via Webflow's hosted MCP.", URL: "https://mcp.webflow.com/mcp", Auth: "oauth"},
		{ID: "MCPX-064", Name: "wolfram", Description: "Wolfram|Alpha computation, math, and curated knowledge.", URL: "https://agenttools.wolfram.com/mcp", Auth: "none"},
		{ID: "MCPX-065", Name: "wordpress-com", Description: "WordPress.com: posts, pages, drafts, stats, and comments.", URL: "https://public-api.wordpress.com/wpcom/v2/mcp/v1", Auth: "oauth"},
	}
}

var mcpxDocRow = regexp.MustCompile("^\\| (MCPX-[0-9]+) \\| `([^`]+)` \\| (.*?) \\| `([^`]+)` \\| [^|]* \\| http `([^`]+)`; auth ([a-z]+)")

// TestMCPXCatalogMatchesManifest keeps the fixture honest: the doc table is the
// source of truth, so a catalog change without this fixture fails here.
func TestMCPXCatalogMatchesManifest(t *testing.T) {
	t.Parallel()
	doc, err := os.ReadFile("../../docs/FEATURE-PARITY.md") // #nosec G304 -- repo file
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	type row struct{ name, url, auth string }
	docRows := make([]row, 0, 65)
	for line := range strings.SplitSeq(string(doc), "\n") {
		m := mcpxDocRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		docRows = append(docRows, row{name: m[2], url: m[5], auth: m[6]})
	}
	if len(docRows) != len(mcpxCatalog()) || len(mcpxCatalog()) != 65 {
		t.Fatalf("manifest rows = %d, fixture = %d, want 65/65", len(docRows), len(mcpxCatalog()))
	}
	for i, want := range docRows {
		got := mcpxCatalog()[i]
		if got.Name != want.name || got.URL != want.url || got.Auth != want.auth {
			t.Errorf("MCPX-%03d = {%s %s %s}, manifest = {%s %s %s}", i+1, got.Name, got.URL, got.Auth, want.name, want.url, want.auth)
		}
	}
}

// TestMCPXCatalogToolsList drives all 65 catalog entries through initialize
// and tools/list against a fake server, then merges the catalogs. Every tool
// name must be unique after the server namespace is applied: a duplicate would
// be an ambiguous tool_search hit (ADR-119).
func TestMCPXCatalogToolsList(t *testing.T) {
	t.Parallel()
	merged := make(map[string]string, len(mcpxCatalog()))
	for _, entry := range mcpxCatalog() {
		srv := newFakeServer()
		srv.name = entry.Name
		tool := entry.Name + "__echo"
		srv.pages = [][]Tool{{{Name: tool, Description: entry.Description, InputSchema: []byte(`{"type":"object","properties":{}}`)}}}
		client, _ := newStdioClient(t, srv, Server{Name: entry.Name, ReadOnly: map[string]bool{tool: true}}, host.Capabilities{})
		tools, err := client.ListTools(context.Background())
		if cerr := client.Close(); cerr != nil {
			t.Errorf("%s: close: %v", entry.Name, cerr)
		}
		if err != nil {
			t.Fatalf("%s: ListTools: %v", entry.Name, err)
		}
		if len(tools) != 1 {
			t.Fatalf("%s: tools = %d, want 1", entry.Name, len(tools))
		}
		name := entry.Name + "/" + tools[0].Name
		if _, dup := merged[name]; dup {
			t.Fatalf("merged catalog duplicates %q", name)
		}
		merged[name] = entry.Name
		if inits, lists, calls := srv.counts(); inits != 1 || lists != 1 || calls != 0 {
			t.Errorf("%s: counts = %d/%d/%d, want one init and one list", entry.Name, inits, lists, calls)
		}
	}
	if len(merged) != 65 {
		t.Fatalf("merged catalog = %d tools, want 65 unique", len(merged))
	}
}
