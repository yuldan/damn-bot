package assemble

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The all-modules P1 fixture (ROADMAP F2, SPEC P1, ADR-066, ADR-121): every
// module with a model-facing deferred group compiled in and enabled
// (all-modules.toml), a 20 KB AGENTS.md, two full memory scopes (2 × 4.000
// stored chars, 6 pinned), 211 installed skills retrieved top-3 against the
// first user message, the maximal volatile tier and the 10 eager schemas.
// Catalog lines, memories and skills are realistic text, not filler.

const allModulesQuery = "Fix the failing test in internal/foo."

// allModulesCatalog is one registry line per module group (≤ 15 tokens each,
// ADR-119), the SPEC §5.2 worst case of 24 groups.
func allModulesCatalog() []CatalogGroup {
	return []CatalogGroup{
		{Name: "ask", Summary: "ask_user one question with choices"},
		{Name: "browser", Summary: "navigate, click, type, read, screenshot pages"},
		{Name: "code", Summary: "symbols, definitions, references, diagnostics, rename"},
		{Name: "computer", Summary: "screen capture, mouse and keyboard control"},
		{Name: "connectors", Summary: "Graph, Feishu, Meet and OSV lookups"},
		{Name: "cron", Summary: "schedule, list, pause and remove recurring jobs"},
		{Name: "delegate", Summary: "delegate_task to a subagent with its own budget"},
		{Name: "docs", Summary: "read and write PDF and office documents"},
		{Name: "exec", Summary: "execute_code in docker, ssh or remote sandboxes"},
		{Name: "gateway", Summary: "list chats, reply and react on chat platforms"},
		{Name: "goals", Summary: "track goals, loops and long-running reviews"},
		{Name: "image", Summary: "image_generate and edit images"},
		{Name: "index", Summary: "trigram code search for large repositories"},
		{Name: "kanban", Summary: "boards, cards, moves and comments"},
		{Name: "memory", Summary: "recall past sessions, propose_memory"},
		{Name: "messaging", Summary: "send_message to a configured channel"},
		{Name: "process", Summary: "process_list, output, wait, send, kill"},
		{Name: "repo", Summary: "repo_map overview of a repository"},
		{Name: "skills", Summary: "search, install and update hub skills"},
		{Name: "speech", Summary: "transcribe audio and speak text"},
		{Name: "undo", Summary: "propose undo of a recorded file action"},
		{Name: "video", Summary: "generate and analyse short videos"},
		{Name: "vision", Summary: "vision_analyze images and screenshots"},
		{Name: "web", Summary: "web_search and extract pages"},
	}
}

// allModulesMemoryScopes returns the approved entries of the user scope and
// the project scope, each filled to the P10 storage cap of 4.000 chars with
// entries ≤ 500 chars; the first three of each scope are pinned.
func allModulesMemoryScopes() [2][]Memory {
	user := []string{
		"User prefers concise answers that list what changed, what was verified and what is still unverified.",
		"User works on Linux amd64 with bash; avoid PowerShell syntax unless a Windows path is involved.",
		"User reviews diffs in the terminal; keep commits small and explain renames in the message body.",
		"User's timezone is Asia/Jakarta (UTC+7); schedule reminders and cron examples in local time.",
		"User writes documentation in Indonesian and code comments in English.",
		"User wants tests run with the race detector before claiming a Go change is done.",
		"User dislikes speculative refactors; touch only what the task needs.",
		"User keeps secrets in pass; never print environment variables that look like tokens.",
		"User prefers table-driven tests with descriptive case names over many small test functions.",
		"User asks for a short plan first when a change touches more than three files.",
	}
	project := []string{
		"Project damnbot is a Go 1.26 module; the composition root is cmd/damnbot and must stay the only package importing everything.",
		"Import edges are enforced by internal/archtest/allowed.go; a new edge needs an ADR in docs/DECISIONS.md.",
		"Golden files are regenerated with go test -update and the diff must be read before committing.",
		"Per-package LOC budgets live in loc.budget; make loc fails when a package exceeds its line.",
		"The bench module has its own go.mod and owns the o200k tokenizer; the root module never requires it.",
		"PR titles follow Conventional Commits and every non-test Go change needs a CHANGELOG entry under Unreleased.",
		"SQLite access is confined to internal/ctxdb and its store subpackages; other packages use consumer interfaces.",
		"Use errors.Is for sentinel errors and wrap with %w; never compare error strings.",
		"CI runs golangci-lint, deadcode, archtest and go test -race on linux, darwin and windows.",
		"Stacked PRs stay under 600 non-test Go lines each; larger changes need the large-change label.",
	}
	return [2][]Memory{fillScope(user, "user"), fillScope(project, "project")}
}

// fillScope pins the first three entries and appends dated decision-log
// entries until the scope holds 4.000 chars.
func fillScope(base []string, scope string) []Memory {
	out := make([]Memory, 0, len(base)+16)
	total := 0
	for i, text := range base {
		out = append(out, Memory{Text: text, Pinned: i < 3})
		total += len(text)
	}
	for day := 1; ; day++ {
		text := "Decision log " + scope + " 2026-10-" + twoDigits(day) +
			": kept the existing behaviour after review; the rationale and the measured numbers are in the linked session."
		if total+len(text) > 4000 {
			break
		}
		out = append(out, Memory{Text: text})
		total += len(text)
	}
	return out
}

func twoDigits(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// allModulesSkill is one installed skill in skills.PromptLine shape.
type allModulesSkill struct{ name, description string }

// allModulesSkills returns the 211 installed skills: every (area, activity)
// pair in order, truncated to 211.
func allModulesSkills() []allModulesSkill {
	areas := []string{
		"api", "ci", "data", "docker", "docs", "frontend", "git", "go", "java", "kubernetes",
		"observability", "performance", "python", "release", "rust", "security", "shell", "sql",
		"terraform", "typescript",
	}
	activities := []struct{ name, what string }{
		{"debugging", "diagnose a crash, hang or wrong output and find the root cause"},
		{"docs", "write or update reference documentation and examples"},
		{"incident", "triage a production incident and write the timeline"},
		{"linting", "configure linters and fix their findings without behaviour changes"},
		{"migration", "migrate to a new major version with a reversible plan"},
		{"packaging", "build, version and publish artifacts reproducibly"},
		{"profiling", "measure CPU and memory hot spots before optimising"},
		{"refactor", "restructure code in small verified steps"},
		{"release", "cut a release with changelog, tags and rollback notes"},
		{"review", "review a change for correctness, style and risk"},
		{"testing", "fix a failing or flaky test and add a regression test"},
	}
	var out []allModulesSkill
	for _, area := range areas {
		for _, act := range activities {
			out = append(out, allModulesSkill{
				name:        area + "-" + act.name,
				description: "Use for " + area + " work: " + act.what + ".",
			})
		}
	}
	return out[:211]
}

// allModulesRetriever ranks like the skill index: query-term overlap with
// name + description, ties by name (deterministic), and returns the memories
// of both scopes pinned first in scope order.
type allModulesRetriever struct{}

func (allModulesRetriever) Memories(context.Context, string) ([]Memory, error) {
	scopes := allModulesMemoryScopes()
	var pinned, ranked []Memory
	for _, scope := range scopes {
		for _, m := range scope {
			if m.Pinned {
				pinned = append(pinned, m)
			} else {
				ranked = append(ranked, m)
			}
		}
	}
	return append(pinned, ranked...), nil
}

func (allModulesRetriever) Skills(_ context.Context, query string) ([]string, error) {
	terms := strings.Fields(strings.ToLower(strings.Trim(query, ".")))
	skills := allModulesSkills()
	score := func(s allModulesSkill) int {
		n := 0
		text := strings.ToLower(s.name + " " + s.description)
		for _, term := range terms {
			if len(term) > 3 && strings.Contains(text, term) {
				n++
			}
		}
		return n
	}
	slices.SortStableFunc(skills, func(a, b allModulesSkill) int {
		if d := score(b) - score(a); d != 0 {
			return d
		}
		return strings.Compare(a.name, b.name)
	})
	lines := make([]string, MaxSkills)
	for i, s := range skills[:MaxSkills] {
		lines[i] = "- " + s.name + ": " + s.description + " (skills/" + s.name + "/SKILL.md)"
	}
	return lines, nil
}

// allModulesAgents is a 20 KB AGENTS.md assembled from real guideline lines.
func allModulesAgents() string {
	lines := []string{
		"- Read a file before editing it and keep edits minimal.",
		"- Run go test -race ./... for the packages you touched before answering.",
		"- Never add a dependency without an ADR and a deps.allow entry.",
		"- Keep exported identifiers documented; comments explain why, not what.",
		"- Use the sandbox reader for every file the agent opens.",
		"- Update CHANGELOG.md under Unreleased for user-visible changes.",
	}
	var b strings.Builder
	b.WriteString("# AGENTS.md\n")
	for section := 1; b.Len() < 20<<10; section++ {
		b.WriteString("\n## Guideline set " + strconv.Itoa(section) + "\n")
		for _, l := range lines {
			b.WriteString(l)
			b.WriteByte('\n')
		}
	}
	return b.String()[:20<<10]
}

func allModulesContext() *ContextInput {
	return &ContextInput{
		Facts:     fillBody("# Environment", "os=linux/amd64 shell=bash cwd=/work/damnbot frontend=tui", CapFacts),
		Workspace: fillBody("# Workspace", "branch=main dirty=true go=1.26.10 verify=go test -race ./...", CapWorkspace),
		Files:     []ContextFile{{Path: "AGENTS.md"}},
		Reader:    fixtureReader{"AGENTS.md": allModulesAgents()},
		Query:     allModulesQuery,
		Retriever: allModulesRetriever{},
	}
}

// allModulesTools is the F2 eager surface: the 8 core tools plus
// tool_search and tool_invoke with the internal/tools schemas (SPEC §5.1).
func allModulesTools() []Tool {
	return append(fixtureTools(),
		Tool{
			Name:        "tool_search",
			Description: "Search deferred tools by name, description or tag; returns up to k full tool schemas.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","minLength":1,"maxLength":200},"group":{"type":"string","minLength":1,"maxLength":64},"k":{"type":"integer","minimum":1,"maximum":5}},"required":["query"],"additionalProperties":false}`),
		},
		Tool{
			Name:        "tool_invoke",
			Description: "Invoke a tool by its exact catalog name with the same approval and sandbox rules as a direct call.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","minLength":1,"maxLength":64},"arguments":{"type":"object"}},"required":["name"],"additionalProperties":false}`),
		},
	)
}

// TestAllModulesFixtureShape pins the inputs SPEC P1 names for all-modules,
// so a fixture edit cannot silently shrink what the golden measures.
func TestAllModulesFixtureShape(t *testing.T) {
	t.Parallel()
	if n := len(allModulesCatalog()); n != CatalogMaxGroups {
		t.Errorf("catalog groups = %d, want %d", n, CatalogMaxGroups)
	}
	checkAllModulesMemory(t)
	skills := allModulesSkills()
	names := map[string]bool{}
	for _, s := range skills {
		names[s.name] = true
	}
	if len(skills) != 211 || len(names) != 211 {
		t.Errorf("skills = %d (%d unique), want 211", len(skills), len(names))
	}
	if n := len(allModulesAgents()); n != 20<<10 {
		t.Errorf("AGENTS.md = %d bytes, want 20 KiB", n)
	}
	lines, err := allModulesRetriever{}.Skills(context.Background(), allModulesQuery)
	if err != nil || len(lines) != MaxSkills || !strings.Contains(lines[0], "-testing:") {
		t.Errorf("top skills for %q = %q, %v", allModulesQuery, lines, err)
	}
}

// checkAllModulesMemory: two full scopes (≤ 4.000 stored chars each,
// entries ≤ 500 chars, P10) with 6 pinned entries in total.
func checkAllModulesMemory(t *testing.T) {
	t.Helper()
	pinned := 0
	for i, scope := range allModulesMemoryScopes() {
		chars := 0
		for _, m := range scope {
			if len(m.Text) > 500 {
				t.Errorf("scope %d: entry of %d chars > 500", i, len(m.Text))
			}
			chars += len(m.Text)
			if m.Pinned {
				pinned++
			}
		}
		if chars > 4000 || chars < 3850 {
			t.Errorf("scope %d stores %d chars, want a full 4.000-char scope", i, chars)
		}
	}
	if pinned != 6 {
		t.Errorf("pinned = %d, want 6", pinned)
	}
}
