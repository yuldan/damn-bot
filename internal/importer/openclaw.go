package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// OpenClawDeps is the I/O seam of one OpenClaw home.
type OpenClawDeps struct {
	ReadFile func(string) ([]byte, error)
	WalkDir  func(string, fs.WalkDirFunc) error
	Redact   func(string) string
	Sink     Sink
}

// OpenClawOptions controls one OpenClaw import.
type OpenClawOptions struct {
	DryRun bool
	Skip   map[string]string
}

// OpenClawImporter imports the safe OpenClaw subset. It is immutable.
type OpenClawImporter struct{ deps OpenClawDeps }

// NewOpenClaw validates deps and returns an OpenClawImporter.
func NewOpenClaw(deps *OpenClawDeps) (*OpenClawImporter, error) {
	if deps == nil {
		return nil, fmt.Errorf("%w: Deps is nil", ErrDeps)
	}
	if deps.ReadFile == nil || deps.WalkDir == nil || deps.Redact == nil || deps.Sink == nil {
		return nil, fmt.Errorf("%w: incomplete Deps", ErrDeps)
	}
	return &OpenClawImporter{deps: *deps}, nil
}

type openclawPlan struct {
	report   Report
	memories []MemoryEntry
	skills   []Skill
}

// Import writes memory and skills; cron and the rest are reported skipped.
func (c *OpenClawImporter) Import(ctx context.Context, root string, opts OpenClawOptions) (Report, error) {
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

func (c *OpenClawImporter) collect(root string, opts OpenClawOptions) (*openclawPlan, error) {
	plan := &openclawPlan{}
	doc, err := c.loadConfig(root)
	if err != nil {
		return nil, err
	}
	if reason, ok := opts.Skip["memory"]; ok {
		plan.report.skip(SourceMemory, "(all)", reason)
	} else if err := c.collectMemory(root, plan); err != nil {
		return nil, err
	}
	if reason, ok := opts.Skip["skills"]; ok {
		plan.report.skip(SourceSkills, "(all)", reason)
	} else if err := c.collectSkills(root, plan); err != nil {
		return nil, err
	}
	if err := c.collectCron(root, doc, plan); err != nil {
		return nil, err
	}
	return plan, c.collectOther(root, doc, plan)
}

func (c *OpenClawImporter) loadConfig(root string) (map[string]any, error) {
	for _, name := range []string{"openclaw.json", "clawdbot.json", "moltbot.json"} {
		path := filepath.Join(root, name)
		raw, err := readOptionalFile(c.deps.ReadFile, path)
		if err != nil {
			return nil, err
		}
		if raw == nil {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, syntaxError(path, err)
		}
		return doc, nil
	}
	return map[string]any{}, nil
}

func (c *OpenClawImporter) collectMemory(root string, plan *openclawPlan) error {
	seen := map[string]bool{}
	found := false
	for _, rel := range []string{"workspace/MEMORY.md", "workspace/USER.md"} {
		ok, err := addRedactedMemoryFile(c.deps.ReadFile, c.deps.Redact, filepath.Join(root, rel), rel, "openclaw-", &plan.report, &plan.memories, seen)
		if err != nil {
			return err
		}
		found = found || ok
	}
	for _, rel := range []string{"workspace/memory"} {
		dir := filepath.Join(root, rel)
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
			_, aerr := addRedactedMemoryFile(c.deps.ReadFile, c.deps.Redact, path, rel+"/"+d.Name(), "openclaw-", &plan.report, &plan.memories, seen)
			return aerr
		})
		if err != nil {
			return err
		}
	}
	if !found {
		plan.report.skip(SourceMemory, "(all)", "no memory sources found")
	}
	return nil
}

func (c *OpenClawImporter) collectSkills(root string, plan *openclawPlan) error {
	found := false
	for _, entry := range []struct{ rel, prefix string }{
		{"workspace/skills", "skills/"},
		{"skills", "shared-skills/"},
		{"workspace/.agents/skills", "shared-skills/"},
	} {
		ok, err := collectSkillTree(c.deps.ReadFile, c.deps.WalkDir, root, filepath.Join(root, entry.rel), entry.prefix,
			&plan.report, SourceSkills, &plan.skills)
		if err != nil {
			return err
		}
		found = found || ok
	}
	if !found {
		plan.report.skip(SourceSkills, "(all)", "no skills directories found")
	}
	return nil
}

func (c *OpenClawImporter) collectCron(root string, doc map[string]any, plan *openclawPlan) error {
	found, err := cronConfigItems(doc, &plan.report)
	if err != nil {
		return err
	}
	storeFound, err := c.cronStoreItems(root, plan)
	if err != nil {
		return err
	}
	if !found && !storeFound {
		plan.report.skip(SourceCron, "(all)", "no cron configuration found")
	}
	return nil
}

func cronConfigItems(doc map[string]any, report *Report) (bool, error) {
	raw, ok := doc["cron"]
	if !ok {
		return false, nil
	}
	switch cron := raw.(type) {
	case map[string]any:
		for _, name := range slices.Sorted(maps.Keys(cron)) {
			report.skip(SourceCron, "cron."+name, "cron store is not available (F4); not written")
		}
	case []any:
		for index, item := range cron {
			key := fmt.Sprintf("cron[%d]", index)
			if fields, ok := item.(map[string]any); ok {
				if name := childString(fields, "name"); name != "" {
					key = "cron." + name
				}
			}
			report.skip(SourceCron, key, "cron store is not available (F4); not written")
		}
	default:
		return false, syntaxError("openclaw.json", errors.New("cron must be an object or a list"))
	}
	return true, nil
}

func (c *OpenClawImporter) cronStoreItems(root string, plan *openclawPlan) (bool, error) {
	dir := filepath.Join(root, "cron")
	entries := 0
	err := c.deps.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if !d.IsDir() {
			entries++
			plan.report.skip(SourceCron, "cron/"+d.Name(), "cron store is not available (F4); not written")
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return entries > 0, nil
}

func (c *OpenClawImporter) collectOther(root string, doc map[string]any, plan *openclawPlan) error {
	for rel, reason := range map[string]string{
		"workspace/AGENTS.md": "workspace instructions have no Damn-Bot target",
		"workspace/SOUL.md":   "SOUL.md has no Damn-Bot target",
	} {
		raw, err := readOptionalFile(c.deps.ReadFile, filepath.Join(root, rel))
		if err != nil {
			return err
		}
		if raw != nil {
			plan.report.skip(SourceConfig, rel, reason)
		}
	}
	for key, reason := range map[string]string{
		"memory":   "memory-backend settings are not imported",
		"skills":   "per-skill config is not imported",
		"hooks":    "hooks are not imported",
		"mcp":      "mcp.servers has no Damn-Bot store yet (F3); not imported",
		"sessions": "session import is not available here; not imported",
	} {
		if _, ok := doc[key]; ok {
			plan.report.skip(SourceConfig, key, reason)
		}
	}
	err := c.deps.WalkDir(filepath.Join(root, "agents"), func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		plan.report.skip(SourceConfig, "agents/", "sub-agent definitions have no Damn-Bot target")
		return fs.SkipDir
	})
	return err
}
