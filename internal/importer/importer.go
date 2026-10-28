package importer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/damnbot/damnbot/internal/skills"
)

// Source names one enumerated input.
type Source string

// Sources enumerated by the importer.
const (
	SourceConfig   Source = "config"
	SourceSkills   Source = "skills"
	SourceMemory   Source = "memory"
	SourceMCP      Source = "mcp"
	SourceCron     Source = "cron"
	SourceSecrets  Source = "secrets"
	SourceSessions Source = "sessions"
)

// Skip is one source item that was not imported, with the reason it was not.
type Skip struct {
	Source Source
	Key    string
	Reason string
}

// Clamp is one mapped numeric value that was outside the Damn-Bot range.
type Clamp struct {
	Key  string
	From int
	To   int
}

// SourceReport is the mechanical account of one source: every enumerated item
// is either imported or listed in Skipped, so Imported+Skipped == Total.
type SourceReport struct {
	Total    int
	Imported int
	Changed  int
	Skipped  []Skip
}

// Report is the result of one [Importer.Import] run.
type Report struct {
	Config   SourceReport
	Skills   SourceReport
	Memory   SourceReport
	MCP      SourceReport
	Cron     SourceReport
	Secrets  SourceReport
	Sessions SourceReport
	Clamps   []Clamp
	// ToS lists the imported entries whose text matched a GAP-GW-07 pattern.
	// They are still imported as pending and never bundled.
	ToS      []ToSFlag
	Warnings []string
}

// Total returns the number of enumerated items across every source.
func (r *Report) Total() int { return r.sum(func(s SourceReport) int { return s.Total }) }

// Imported returns the number of items handled by the run.
func (r *Report) Imported() int { return r.sum(func(s SourceReport) int { return s.Imported }) }

// SkippedCount returns the number of items listed as skipped.
func (r *Report) SkippedCount() int {
	return r.sum(func(s SourceReport) int { return len(s.Skipped) })
}

// Changed returns the number of writes that changed target state.
func (r *Report) Changed() int { return r.sum(func(s SourceReport) int { return s.Changed }) }

// Coverage is the share of enumerated items that were accounted for. It is 1
// when every item is imported or skipped with a reason, and 1 for an empty run.
func (r *Report) Coverage() float64 {
	if total := r.Total(); total > 0 {
		return float64(r.Imported()+r.SkippedCount()) / float64(total)
	}
	return 1
}

func (r *Report) sum(f func(SourceReport) int) int {
	return f(r.Config) + f(r.Skills) + f(r.Memory) + f(r.MCP) + f(r.Cron) + f(r.Secrets) + f(r.Sessions)
}

// source returns the report block for s.
func (r *Report) source(s Source) *SourceReport {
	switch s {
	case SourceConfig:
		return &r.Config
	case SourceSkills:
		return &r.Skills
	case SourceMemory:
		return &r.Memory
	case SourceMCP:
		return &r.MCP
	case SourceCron:
		return &r.Cron
	case SourceSecrets:
		return &r.Secrets
	case SourceSessions:
		return &r.Sessions
	}
	return &r.Secrets
}

func (r *Report) skip(s Source, key, reason string) {
	sr := r.source(s)
	sr.Total++
	sr.Skipped = append(sr.Skipped, Skip{Source: s, Key: key, Reason: reason})
}

func (r *Report) item(s Source) { r.source(s).Total++ }

func (r *Report) sortSkips() {
	slices.SortFunc(r.Config.Skipped, compareSkip)
	slices.SortFunc(r.Skills.Skipped, compareSkip)
	slices.SortFunc(r.Memory.Skipped, compareSkip)
	slices.SortFunc(r.MCP.Skipped, compareSkip)
	slices.SortFunc(r.Cron.Skipped, compareSkip)
	slices.SortFunc(r.Secrets.Skipped, compareSkip)
	slices.SortFunc(r.Sessions.Skipped, compareSkip)
	slices.SortFunc(r.Clamps, func(a, b Clamp) int { return strings.Compare(a.Key, b.Key) })
	slices.SortFunc(r.ToS, func(a, b ToSFlag) int {
		if a.Source != b.Source {
			return strings.Compare(string(a.Source), string(b.Source))
		}
		return strings.Compare(a.Key, b.Key)
	})
}

func compareSkip(a, b Skip) int {
	if a.Source != b.Source {
		return strings.Compare(string(a.Source), string(b.Source))
	}
	return strings.Compare(a.Key, b.Key)
}

// ToSFlag is one imported skill whose text matched a GAP-GW-07 pattern. The
// import report prints it so a dry run shows the risk before anything is
// written.
type ToSFlag struct {
	Source Source
	Key    string
	Risk   skills.ToSRisk
	Note   string
}

// ToSFlags returns the ToS flags in deterministic order.
func (r *Report) ToSFlags() []ToSFlag { return r.ToS }

// tos records one high-risk item. Lower levels are not printed: they carry no
// acknowledgement requirement and would only add noise to the report.
func (r *Report) tos(s Source, key string, meta skills.ToSMeta) {
	if meta.Risk != skills.ToSHigh {
		return
	}
	r.ToS = append(r.ToS, ToSFlag{Source: s, Key: key, Risk: meta.Risk, Note: meta.Note})
}

// ConfigEntry is one mapped config key.
type ConfigEntry struct {
	Section string
	Key     string
	Value   any
}

// Skill is one imported skill. An imported skill is always quarantined as
// pending review; the sink owns that policy, so the item carries no status.
type Skill struct {
	Name        string
	Description string
	Body        string
	Origin      string
	// SHA256 is the digest of the SKILL.md bytes this item was read from.
	SHA256 string
	// ToSRisk and ToSNote are the GAP-GW-07 metadata of this entry. A high
	// risk is reported and the skill stays pending; it is never bundled.
	ToSRisk skills.ToSRisk
	ToSNote string
	// Data is the raw SKILL.md, so the skills service can parse the
	// optional side_effects/requires frontmatter instead of losing it.
	Data []byte
	// Runtime names external runtimes referenced by the skill body.
	Runtime []string
}

// MemoryEntry is one curated memory entry.
type MemoryEntry struct {
	Store string
	Text  string
}

// MCPServer is one imported MCP server definition. Env values that carried a
// secret hold a keychain reference instead of the value.
type MCPServer struct {
	Name     string
	Command  string
	Args     []string
	URL      string
	Env      map[string]string
	Headers  map[string]string
	TimeoutS int
}

// Schedule is a parsed cron schedule.
type Schedule struct {
	Kind    string
	Expr    string
	Minutes int
	RunAt   string
}

// CronJob is one imported cron job. Imported jobs are always disabled until
// the user enables them.
type CronJob struct {
	ID       string
	Name     string
	Prompt   string
	Deliver  string
	Workdir  string
	Schedule Schedule
	Enabled  bool
}

// Sink receives imported items. A method reports whether it changed state so a
// second run can be proven idempotent.
type Sink interface {
	PutConfig(ctx context.Context, e ConfigEntry) (bool, error)
	PutSkill(ctx context.Context, s *Skill) (bool, error)
	PutMemory(ctx context.Context, m MemoryEntry) (bool, error)
	PutMCPServer(ctx context.Context, s MCPServer) (bool, error)
	PutCron(ctx context.Context, j CronJob) (bool, error)
}

// SessionSink receives imported sessions.
type SessionSink interface {
	PutSession(ctx context.Context, s *Session) (bool, error)
}

// Secrets is the keychain seam; values are never persisted in reports.
type Secrets interface {
	Put(ctx context.Context, name, value string) (bool, error)
}

// Deps is the explicit dependency set of an [Importer] (E-11).
type Deps struct {
	ReadFile    func(string) ([]byte, error)
	WalkDir     func(string, fs.WalkDirFunc) error
	LookPath    func(string) (string, error)
	Sink        Sink
	SessionSink SessionSink
	Secrets     Secrets
	Sessions    SessionSource
}

// Options controls one import run.
type Options struct {
	// DryRun enumerates and reports without writing anything.
	DryRun bool
	// Unsupported names sources the caller cannot persist yet; every item is
	// enumerated and reported as skipped with the given reason, never written.
	Unsupported map[Source]string
}

// Importer imports one Hermes home. It is immutable and safe for concurrent
// use when its dependencies are.
type Importer struct {
	deps Deps
}

// New validates deps and returns an Importer.
func New(deps *Deps) (*Importer, error) {
	if deps == nil {
		return nil, fmt.Errorf("%w: Deps is nil", ErrDeps)
	}
	switch {
	case deps.ReadFile == nil:
		return nil, fmt.Errorf("%w: Deps.ReadFile is nil", ErrDeps)
	case deps.WalkDir == nil:
		return nil, fmt.Errorf("%w: Deps.WalkDir is nil", ErrDeps)
	case deps.Sink == nil:
		return nil, fmt.Errorf("%w: Deps.Sink is nil", ErrDeps)
	case deps.SessionSink == nil:
		return nil, fmt.Errorf("%w: Deps.SessionSink is nil", ErrDeps)
	case deps.Secrets == nil:
		return nil, fmt.Errorf("%w: Deps.Secrets is nil", ErrDeps)
	case deps.Sessions == nil:
		return nil, fmt.Errorf("%w: Deps.Sessions is nil", ErrDeps)
	}
	return &Importer{deps: *deps}, nil
}

// Import reads the Hermes home rooted at root. A parse failure returns
// [ErrSyntax] before any write, so a broken source leaves no partial import.
func (i *Importer) Import(ctx context.Context, root string, opts Options) (Report, error) {
	plan, err := i.collect(ctx, root, opts)
	if err != nil {
		return Report{}, err
	}
	return i.apply(ctx, plan, opts)
}

func readOptionalFile(read func(string) ([]byte, error), path string) ([]byte, error) {
	data, err := read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("importer: read %s: %w", path, err)
	}
	return data, nil
}

func (i *Importer) readOptional(path string) ([]byte, error) {
	return readOptionalFile(i.deps.ReadFile, path)
}

// WarningsList returns non-fatal import warnings.
func (r *Report) WarningsList() []string { return r.Warnings }
