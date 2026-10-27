package importer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	claudeSettings = "settings"
	claudeMCP      = "mcp"
	claudeMemory   = "memory"
	claudeSkills   = "skills"
	claudeCommands = "commands"
)

// claudeSource maps a category to its accounting slot; the CLI prints labels.
func claudeSource(category string) Source {
	switch category {
	case claudeSettings:
		return SourceConfig
	case claudeMCP:
		return SourceMCP
	case claudeMemory:
		return SourceMemory
	case claudeSkills:
		return SourceSkills
	}
	return SourceCron
}

func claudeSkip(report *Report, category, key, reason string) {
	report.skip(claudeSource(category), key, reason)
}

func claudeItem(report *Report, category string) { report.item(claudeSource(category)) }

// ClaudeOptions controls one Claude Code import.
type ClaudeOptions struct {
	DryRun bool
	// Skip marks a supported category this run must not write (`--only`).
	Skip map[string]string
}

// ClaudeDeps is the I/O seam of one Claude Code home.
type ClaudeDeps struct {
	ReadFile func(string) ([]byte, error)
	WalkDir  func(string, fs.WalkDirFunc) error
	Sink     Sink
}

// ClaudeImporter imports one Claude Code home. It is immutable.
type ClaudeImporter struct{ deps ClaudeDeps }

// NewClaude validates deps and returns a ClaudeImporter.
func NewClaude(deps *ClaudeDeps) (*ClaudeImporter, error) {
	if deps == nil {
		return nil, fmt.Errorf("%w: Deps is nil", ErrDeps)
	}
	if deps.ReadFile == nil || deps.WalkDir == nil || deps.Sink == nil {
		return nil, fmt.Errorf("%w: incomplete Deps", ErrDeps)
	}
	return &ClaudeImporter{deps: *deps}, nil
}

type claudePlan struct {
	report   Report
	memories []MemoryEntry
	skills   []Skill
}

// Import reads one ~/.claude home; only memory and skills have a write seam.
func (c *ClaudeImporter) Import(ctx context.Context, root string, opts ClaudeOptions) (Report, error) {
	plan, err := c.collect(root, opts)
	if err != nil {
		return Report{}, err
	}
	report := plan.report
	if err := finishMemorySkills(ctx, &report, plan.memories, plan.skills, c.deps.Sink, opts.DryRun); err != nil {
		return report, err
	}
	return report, nil
}

func (c *ClaudeImporter) collect(root string, opts ClaudeOptions) (*claudePlan, error) {
	plan := &claudePlan{}
	if err := c.collectSettingsAndMCP(root, plan); err != nil {
		return nil, err
	}
	if reason, ok := opts.Skip[claudeMemory]; ok {
		claudeSkip(&plan.report, claudeMemory, "(all)", reason)
	} else if err := c.collectMemory(root, plan); err != nil {
		return nil, err
	}
	if reason, ok := opts.Skip[claudeSkills]; ok {
		claudeSkip(&plan.report, claudeSkills, "(all)", reason)
	} else if err := c.collectSkills(root, plan); err != nil {
		return nil, err
	}
	claudeSkip(&plan.report, claudeCommands, "(all)", "Claude slash commands have no Damn-Bot equivalent; not imported")
	return plan, nil
}

func (c *ClaudeImporter) collectSettingsAndMCP(root string, plan *claudePlan) error {
	path := filepath.Join(root, "settings.json")
	raw, err := readOptionalFile(c.deps.ReadFile, path)
	if err != nil {
		return err
	}
	doc := map[string]any{}
	if raw == nil {
		claudeSkip(&plan.report, claudeSettings, "(all)", "no settings.json found")
	} else if err := json.Unmarshal(raw, &doc); err != nil {
		return syntaxError(path, err)
	} else if _, ok := doc["permissions"]; ok {
		claudeSkip(&plan.report, claudeSettings, "permissions",
			"not migrated: no config writer in F3 and Claude shell globs have no argv-exact approval.shell_allow mapping")
	} else {
		claudeSkip(&plan.report, claudeSettings, "(all)", "no permission rules found")
	}
	names := map[string]bool{}
	if servers, ok := doc["mcpServers"].(map[string]any); ok {
		for name := range servers {
			names[name] = true
		}
	}
	if len(names) == 0 {
		claudeSkip(&plan.report, claudeMCP, "(all)", "no MCP servers found")
	}
	for name := range names {
		claudeSkip(&plan.report, claudeMCP, name, "MCP store is not available (F5); not written")
	}
	return nil
}

func (c *ClaudeImporter) collectMemory(root string, plan *claudePlan) error {
	path := filepath.Join(root, "CLAUDE.md")
	raw, err := readOptionalFile(c.deps.ReadFile, path)
	if err != nil {
		return err
	}
	if raw == nil {
		claudeSkip(&plan.report, claudeMemory, "(all)", "no CLAUDE.md found")
		return nil
	}
	if !utf8.Valid(raw) {
		return syntaxError(path, errors.New("file is not valid UTF-8"))
	}
	entries := extractMarkdownEntries(string(raw))
	for index, entry := range entries {
		if n := utf8.RuneCountInString(entry); n > maxMemoryChars {
			claudeSkip(&plan.report, claudeMemory, fmt.Sprintf("entry %d", index+1),
				fmt.Sprintf("%d chars exceeds the %d-char memory cap", n, maxMemoryChars))
			continue
		}
		plan.memories = append(plan.memories, MemoryEntry{Store: "claude-md", Text: entry})
		claudeItem(&plan.report, claudeMemory)
	}
	if len(entries) == 0 {
		claudeSkip(&plan.report, claudeMemory, "(all)", "no importable entries in CLAUDE.md")
	}
	return nil
}

func (c *ClaudeImporter) collectSkills(root string, plan *claudePlan) error {
	found, err := collectSkillTree(c.deps.ReadFile, c.deps.WalkDir, root, filepath.Join(root, "skills"), "",
		&plan.report, SourceSkills, &plan.skills)
	if err != nil {
		return err
	}
	if !found {
		claudeSkip(&plan.report, claudeSkills, "(all)", "no skills with SKILL.md found")
	}
	return nil
}

// extractMarkdownEntries mirrors hermes_cli/agent_import.py markdown rules.
func extractMarkdownEntries(text string) []string {
	m := &claudeMarkdown{}
	for line := range strings.SplitSeq(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		m.read(line)
	}
	m.flush()
	var out []string
	seen := map[string]bool{}
	for _, entry := range m.entries {
		key := strings.ToLower(strings.Join(strings.Fields(entry), " "))
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, strings.TrimSpace(entry))
	}
	return out
}

type claudeMarkdown struct {
	entries   []string
	headings  []string
	paragraph []string
	inCode    bool
}

func (m *claudeMarkdown) add(content string) {
	var kept []string
	for _, heading := range m.headings {
		if heading != "" && !excludedClaudeHeading(heading) {
			kept = append(kept, heading)
		}
	}
	if prefix := strings.Join(kept, " > "); prefix != "" {
		m.entries = append(m.entries, prefix+": "+content)
		return
	}
	m.entries = append(m.entries, content)
}

func (m *claudeMarkdown) flush() {
	block := strings.TrimSpace(strings.Join(m.paragraph, " "))
	m.paragraph = nil
	if block != "" {
		m.add(block)
	}
}

func (m *claudeMarkdown) read(line string) {
	stripped := strings.TrimSpace(line)
	if strings.HasPrefix(stripped, "```") {
		m.inCode = !m.inCode
		m.flush()
		return
	}
	if m.inCode {
		return
	}
	if level, heading, ok := claudeHeading(stripped); ok {
		m.flush()
		if len(m.headings) >= level {
			m.headings = append(m.headings[:level-1], heading)
		} else {
			m.headings = append(m.headings, heading)
		}
		return
	}
	if bullet := claudeBullet(line); bullet != "" {
		m.flush()
		m.add(bullet)
		return
	}
	if stripped == "" || (strings.HasPrefix(stripped, "|") && strings.HasSuffix(stripped, "|")) {
		m.flush()
		return
	}
	m.paragraph = append(m.paragraph, stripped)
}

func claudeHeading(line string) (int, string, bool) {
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level >= len(line) || line[level] != ' ' {
		return 0, "", false
	}
	text := strings.TrimSpace(line[level:])
	return level, text, text != ""
}

func claudeBullet(line string) string {
	trimmed := strings.TrimLeft(line, " \t")
	if len(trimmed) > 1 && (trimmed[0] == '-' || trimmed[0] == '*') && trimmed[1] == ' ' {
		return strings.TrimSpace(trimmed[2:])
	}
	digits := 0
	for digits < len(trimmed) && trimmed[digits] >= '0' && trimmed[digits] <= '9' {
		digits++
	}
	if digits > 0 && digits+1 < len(trimmed) && trimmed[digits] == '.' && trimmed[digits+1] == ' ' {
		return strings.TrimSpace(trimmed[digits+2:])
	}
	return ""
}

func excludedClaudeHeading(heading string) bool {
	upper := strings.ToUpper(heading)
	for _, name := range []string{"MEMORY.MD", "USER.MD", "SOUL.MD", "AGENTS.MD", "TOOLS.MD", "IDENTITY.MD", "CLAUDE.MD"} {
		if strings.Contains(upper, name) {
			return true
		}
	}
	return false
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
