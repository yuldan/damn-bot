package importer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

type secretPut struct {
	name  string
	value string
}

type collected struct {
	report          Report
	configDoc       map[string]any
	configAmbiguous map[string]string
	entries         []ConfigEntry
	skills          []Skill
	memories        []MemoryEntry
	servers         []MCPServer
	jobs            []CronJob
	secrets         []secretPut
	sessions        []Session
	disabled        map[Source]string
}

func (i *Importer) collect(ctx context.Context, root string, opts Options) (*collected, error) {
	c := &collected{disabled: opts.Unsupported}
	raw, err := i.readOptional(filepath.Join(root, "config.yaml"))
	if err != nil {
		return nil, err
	}
	if raw != nil {
		doc, ambiguous, derr := decodeMapping(raw, filepath.Join(root, "config.yaml"))
		if derr != nil {
			return nil, derr
		}
		c.configDoc, c.configAmbiguous = doc, ambiguous
	}
	if err := i.collectConfig(c); err != nil {
		return nil, err
	}
	if err := i.collectSkills(root, c); err != nil {
		return nil, err
	}
	if err := i.collectMemory(root, c); err != nil {
		return nil, err
	}
	if err := i.collectMCP(c); err != nil {
		return nil, err
	}
	if err := i.collectCron(root, c); err != nil {
		return nil, err
	}
	if err := i.collectSecrets(root, c); err != nil {
		return nil, err
	}
	if err := i.collectSessions(ctx, root, c); err != nil {
		return nil, err
	}
	c.report.sortSkips()
	return c, nil
}

// reject reports one enumerated item of s as skipped when s is unsupported.
func (c *collected) reject(s Source, key string) bool {
	reason, ok := c.disabled[s]
	if !ok {
		return false
	}
	c.report.skip(s, key, reason)
	return true
}

func (i *Importer) apply(ctx context.Context, c *collected, opts Options) (Report, error) {
	r := c.report
	if opts.DryRun {
		r.Config.Imported, r.Skills.Imported, r.Memory.Imported = len(c.entries), len(c.skills), len(c.memories)
		r.MCP.Imported, r.Cron.Imported, r.Secrets.Imported = len(c.servers), len(c.jobs), len(c.secrets)
		r.Sessions.Imported = len(c.sessions)
		return r, nil
	}
	for index := range c.entries {
		entry := c.entries[index]
		changed, err := i.deps.Sink.PutConfig(ctx, entry)
		if stop, rerr := record(&r, SourceConfig, entry.Section+"."+entry.Key, changed, err); stop {
			return r, rerr
		}
	}
	for index := range c.skills {
		skill := c.skills[index]
		changed, err := i.deps.Sink.PutSkill(ctx, &skill)
		if stop, rerr := record(&r, SourceSkills, skill.Name, changed, err); stop {
			return r, rerr
		}
	}
	for index := range c.memories {
		entry := c.memories[index]
		changed, err := i.deps.Sink.PutMemory(ctx, entry)
		if stop, rerr := record(&r, SourceMemory, entry.Store, changed, err); stop {
			return r, rerr
		}
	}
	for index := range c.servers {
		server := c.servers[index]
		changed, err := i.deps.Sink.PutMCPServer(ctx, server)
		if stop, rerr := record(&r, SourceMCP, server.Name, changed, err); stop {
			return r, rerr
		}
	}
	for index := range c.jobs {
		job := c.jobs[index]
		changed, err := i.deps.Sink.PutCron(ctx, job)
		if stop, rerr := record(&r, SourceCron, job.Name, changed, err); stop {
			return r, rerr
		}
	}
	for _, s := range c.secrets {
		changed, err := i.deps.Secrets.Put(ctx, s.name, s.value)
		if stop, rerr := record(&r, SourceSecrets, s.name, changed, err); stop {
			return r, rerr
		}
	}
	if stop, rerr := i.applySessions(ctx, &r, c.sessions); stop {
		return r, rerr
	}
	r.sortSkips()
	return r, nil
}

func (i *Importer) applySessions(ctx context.Context, r *Report, sessions []Session) (bool, error) {
	for index := range sessions {
		session := sessions[index]
		changed, err := i.deps.SessionSink.PutSession(ctx, &session)
		if stop, rerr := record(r, SourceSessions, session.SourceID, changed, err); stop {
			return true, rerr
		}
	}
	return false, nil
}

// addRedactedMemoryFile parses one markdown memory file and dedups by redacted key.
func addRedactedMemoryFile(read func(string) ([]byte, error), redact func(string) string,
	path, label, storePrefix string, report *Report, out *[]MemoryEntry, seen map[string]bool,
) (bool, error) {
	raw, err := readOptionalFile(read, path)
	if err != nil {
		return false, err
	}
	if raw == nil {
		return false, nil
	}
	if !utf8.Valid(raw) {
		return false, syntaxError(path, errors.New("file is not valid UTF-8"))
	}
	for index, entry := range extractMarkdownEntries(string(raw)) {
		key := strings.ToLower(strings.Join(strings.Fields(redact(entry)), " "))
		if seen[key] {
			report.skip(SourceMemory, fmt.Sprintf("%s[%d]", label, index+1), "duplicate redacted content")
			continue
		}
		if n := utf8.RuneCountInString(entry); n > maxMemoryChars {
			report.skip(SourceMemory, fmt.Sprintf("%s[%d]", label, index+1),
				fmt.Sprintf("%d chars exceeds the %d-char memory cap", n, maxMemoryChars))
			continue
		}
		seen[key] = true
		*out = append(*out, MemoryEntry{Store: storePrefix + label, Text: entry})
		report.item(SourceMemory)
	}
	return true, nil
}

// finishMemorySkills returns the dry-run accounting or writes both plan slices.
func finishMemorySkills(ctx context.Context, report *Report, memories []MemoryEntry, skills []Skill, sink Sink, dryRun bool) error {
	if dryRun {
		report.Memory.Imported, report.Skills.Imported = len(memories), len(skills)
		return nil
	}
	return writeMemorySkills(ctx, report, memories, skills, sink)
}

// writeMemorySkills applies the two plan slices shared by the agent importers.
func writeMemorySkills(ctx context.Context, report *Report, memories []MemoryEntry, skills []Skill, sink Sink) error {
	for index := range memories {
		entry := memories[index]
		changed, err := sink.PutMemory(ctx, entry)
		if stop, rerr := record(report, SourceMemory, entry.Store, changed, err); stop {
			return rerr
		}
	}
	for index := range skills {
		skill := skills[index]
		changed, err := sink.PutSkill(ctx, &skill)
		if stop, rerr := record(report, SourceSkills, skill.Name, changed, err); stop {
			return rerr
		}
	}
	return nil
}

// record accounts one sink result; a [SkipError] becomes a reported skip.
func record(r *Report, s Source, key string, changed bool, err error) (stop bool, rerr error) {
	if err == nil {
		sr := r.source(s)
		sr.Imported++
		if changed {
			sr.Changed++
		}
		return false, nil
	}
	reason, skip := "", false
	if se, ok := errors.AsType[*SkipError](err); ok {
		reason, skip = se.Reason, true
	}
	if !skip {
		return true, writeError(s, key, err)
	}
	sr := r.source(s)
	sr.Skipped = append(sr.Skipped, Skip{Source: s, Key: key, Reason: reason})
	return false, nil
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
