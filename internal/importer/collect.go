package importer

import (
	"context"
	"fmt"
	"path/filepath"
)

type secretPut struct {
	name  string
	value string
}

type collected struct {
	report    Report
	configDoc map[string]any
	entries   []ConfigEntry
	skills    []Skill
	memories  []MemoryEntry
	servers   []MCPServer
	jobs      []CronJob
	secrets   []secretPut
}

func (i *Importer) collect(root string) (*collected, error) {
	c := &collected{}
	raw, err := i.readOptional(filepath.Join(root, "config.yaml"))
	if err != nil {
		return nil, err
	}
	if raw != nil {
		doc, err := decodeMapping(raw, filepath.Join(root, "config.yaml"))
		if err != nil {
			return nil, err
		}
		c.configDoc = doc
	}
	if err := i.collectConfig(c); err != nil {
		return nil, err
	}
	c.report.sortSkips()
	return c, nil
}

func (i *Importer) apply(ctx context.Context, c *collected, opts Options) (Report, error) {
	r := c.report
	if opts.DryRun {
		r.Config.Imported, r.Skills.Imported, r.Memory.Imported = len(c.entries), len(c.skills), len(c.memories)
		r.MCP.Imported, r.Cron.Imported, r.Secrets.Imported = len(c.servers), len(c.jobs), len(c.secrets)
		return r, nil
	}
	for index := range c.entries {
		entry := c.entries[index]
		changed, err := i.deps.Sink.PutConfig(ctx, entry)
		if err != nil {
			return r, writeError(SourceConfig, entry.Section+"."+entry.Key, err)
		}
		count(&r.Config, changed)
	}
	for index := range c.skills {
		skill := c.skills[index]
		changed, err := i.deps.Sink.PutSkill(ctx, skill)
		if err != nil {
			return r, writeError(SourceSkills, skill.Name, err)
		}
		count(&r.Skills, changed)
	}
	for index := range c.memories {
		entry := c.memories[index]
		changed, err := i.deps.Sink.PutMemory(ctx, entry)
		if err != nil {
			return r, writeError(SourceMemory, entry.Store, err)
		}
		count(&r.Memory, changed)
	}
	for index := range c.servers {
		server := c.servers[index]
		changed, err := i.deps.Sink.PutMCPServer(ctx, server)
		if err != nil {
			return r, writeError(SourceMCP, server.Name, err)
		}
		count(&r.MCP, changed)
	}
	for index := range c.jobs {
		job := c.jobs[index]
		changed, err := i.deps.Sink.PutCron(ctx, job)
		if err != nil {
			return r, writeError(SourceCron, job.Name, err)
		}
		count(&r.Cron, changed)
	}
	for _, s := range c.secrets {
		changed, err := i.deps.Secrets.Put(ctx, s.name, s.value)
		if err != nil {
			return r, writeError(SourceSecrets, s.name, err)
		}
		count(&r.Secrets, changed)
	}
	return r, nil
}

func count(r *SourceReport, changed bool) {
	r.Imported++
	if changed {
		r.Changed++
	}
}

func writeError(s Source, key string, err error) error {
	return fmt.Errorf("%w: write %s %q: %w", ErrSource, s, key, err)
}

func (c *collected) queueSecret(name, value string) {
	for _, existing := range c.secrets {
		if existing.name == name {
			return
		}
	}
	c.secrets = append(c.secrets, secretPut{name: name, value: value})
	c.report.item(SourceSecrets)
}
