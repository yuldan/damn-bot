package importer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Codex categories.
const (
	codexMCP    = "mcp"
	codexMemory = "memory"
	codexSkills = "skills"
)

// CodexDeps is the I/O seam of one Codex home. Redact is the session redactor;
// it defines the duplicate key across AGENTS.md and memories/*.md.
type CodexDeps struct {
	ReadFile func(string) ([]byte, error)
	WalkDir  func(string, fs.WalkDirFunc) error
	Redact   func(string) string
	Sink     Sink
}

// CodexOptions controls one Codex import.
type CodexOptions struct {
	DryRun bool
	// Skip marks a supported category this run must not write (`--only`).
	Skip map[string]string
}

// CodexImporter imports one Codex home. It is immutable.
type CodexImporter struct{ deps CodexDeps }

// NewCodex validates deps and returns a CodexImporter.
func NewCodex(deps *CodexDeps) (*CodexImporter, error) {
	if deps == nil {
		return nil, fmt.Errorf("%w: Deps is nil", ErrDeps)
	}
	if deps.ReadFile == nil || deps.WalkDir == nil || deps.Redact == nil || deps.Sink == nil {
		return nil, fmt.Errorf("%w: incomplete Deps", ErrDeps)
	}
	return &CodexImporter{deps: *deps}, nil
}

type codexPlan struct {
	report   Report
	memories []MemoryEntry
	skills   []Skill
}

// Import reads one ~/.codex home and writes memory and skills. mcp_servers is
// enumerated and reported skipped until the MCP store exists (F5).
func (c *CodexImporter) Import(ctx context.Context, root string, opts CodexOptions) (Report, error) {
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

func (c *CodexImporter) collect(root string, opts CodexOptions) (*codexPlan, error) {
	plan := &codexPlan{}
	if err := c.collectMCP(root, plan); err != nil {
		return nil, err
	}
	if reason, ok := opts.Skip[codexMemory]; ok {
		plan.report.skip(SourceMemory, "(all)", reason)
	} else if err := c.collectMemory(root, plan); err != nil {
		return nil, err
	}
	if reason, ok := opts.Skip[codexSkills]; ok {
		plan.report.skip(SourceSkills, "(all)", reason)
	} else if err := c.collectSkills(root, plan); err != nil {
		return nil, err
	}
	return plan, nil
}

func (c *CodexImporter) collectMCP(root string, plan *codexPlan) error {
	path := filepath.Join(root, "config.toml")
	raw, err := readOptionalFile(c.deps.ReadFile, path)
	if err != nil {
		return err
	}
	if raw == nil {
		plan.report.skip(SourceMCP, "(all)", "no config.toml found")
		return nil
	}
	var config struct {
		MCPServers map[string]any `toml:"mcp_servers"`
	}
	if _, err := toml.Decode(string(raw), &config); err != nil {
		return syntaxError(path, err)
	}
	if len(config.MCPServers) == 0 {
		plan.report.skip(SourceMCP, "(all)", "no mcp_servers found")
		return nil
	}
	for name := range config.MCPServers {
		plan.report.skip(SourceMCP, name, "MCP store is not available (F5); not written")
	}
	return nil
}

func (c *CodexImporter) collectMemory(root string, plan *codexPlan) error {
	seen := map[string]bool{}
	if _, err := addRedactedMemoryFile(c.deps.ReadFile, c.deps.Redact, filepath.Join(root, "AGENTS.md"), "AGENTS.md", "codex-", &plan.report, &plan.memories, seen); err != nil {
		return err
	}
	dir := filepath.Join(root, "memories")
	found := false
	err := c.deps.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if d.IsDir() || filepath.Dir(path) != dir || !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		found = true
		_, aerr := addRedactedMemoryFile(c.deps.ReadFile, c.deps.Redact, path, "memories/"+d.Name(), "codex-", &plan.report, &plan.memories, seen)
		return aerr
	})
	if err != nil {
		return err
	}
	if !found {
		plan.report.skip(SourceMemory, "memories", "no memories/*.md found")
	}
	return nil
}

func (c *CodexImporter) collectSkills(root string, plan *codexPlan) error {
	found, err := collectSkillTree(c.deps.ReadFile, c.deps.WalkDir, root, filepath.Join(root, "skills"), "",
		&plan.report, SourceSkills, &plan.skills)
	if err != nil {
		return err
	}
	if !found {
		plan.report.skip(SourceSkills, "(all)", "no skills with SKILL.md found")
	}
	return nil
}
