//go:build !minimal

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/damnbot/damnbot/internal/auto"
	"github.com/damnbot/damnbot/internal/boundary"
	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/provider"
)

// serveAutoTick is the non-critical cadence of the serve automation lane. It
// is independent of the gateway idle loop and does nothing when every
// [modules.auto.<feature>] section is off (the default).
const serveAutoTick = time.Minute

// serveAuto owns the F8 automation scheduler inside `damnbot serve`. It is
// built once and started with the daemon; Tick runs only registered (opt-in)
// tasks and never touches a turn.
type serveAuto struct {
	cfg     auto.Config
	svc     *auto.Service
	sch     *auto.Scheduler
	dataDir string
	now     func() time.Time
}

// newServeAuto maps the typed config to the closed auto registry and registers
// the serve-owned tasks. No task is registered while its feature is off.
func newServeAuto(cfg *config.Config, dataDir string, now func() time.Time) (*serveAuto, error) {
	if now == nil {
		now = time.Now
	}
	ac := autoConfig(cfg)
	svc, err := auto.New(ac, nil, auto.WithNow(now))
	if err != nil {
		return nil, fmt.Errorf("auto config: %w", err)
	}
	a := &serveAuto{cfg: ac, svc: svc, sch: auto.NewScheduler(now), dataDir: dataDir, now: now}
	if svc.Enabled(auto.DiskCleanup) {
		if err := a.sch.Every(auto.DiskCleanup, 24*time.Hour, a.cleanup); err != nil {
			return nil, fmt.Errorf("auto cleanup schedule: %w", err)
		}
	}
	if svc.Enabled(auto.Maintenance) {
		if err := a.sch.Every(auto.Maintenance, 24*time.Hour, a.maintain); err != nil {
			return nil, fmt.Errorf("auto maintenance schedule: %w", err)
		}
	}
	return a, nil
}

// autoConfig is the single config→registry mapping of the automation lane.
func autoConfig(cfg *config.Config) auto.Config {
	m := cfg.Modules.Auto
	limits := map[auto.Feature]auto.Limits{
		auto.Review:          limitsOf(m.Review),
		auto.Title:           limitsOf(m.Title),
		auto.Curator:         limitsOf(m.Curator),
		auto.Compaction:      limitsOf(m.Compaction),
		auto.Blueprints:      limitsOf(m.Blueprints),
		auto.Suggestions:     limitsOf(m.Suggestions),
		auto.Goals:           limitsOf(m.Goals),
		auto.Loops:           limitsOf(m.Loops),
		auto.Heartbeat:       limitsOf(m.Heartbeat),
		auto.DiskCleanup:     limitsOf(m.DiskCleanup),
		auto.Maintenance:     limitsOf(m.Maintenance),
		auto.OutboundWebhook: limitsOf(m.Webhooks),
	}
	return auto.Config{Model: cfg.Provider.Model, Features: limits}
}

func limitsOf(f config.ModulesAutoFeature) auto.Limits {
	return auto.Limits{
		Enabled:        f.Enabled,
		DailyTokens:    f.DailyTokens,
		DailyUSDMicros: int64(f.DailyUSD * 1e6),
	}
}

// Tick runs one scheduler pass (the unit of the serve ticker).
func (a *serveAuto) Tick(ctx context.Context) []auto.TickResult { return a.sch.Tick(ctx) }

// Run ticks until ctx is cancelled; it returns nil on a clean stop.
func (a *serveAuto) Run(ctx context.Context) error {
	if err := a.sch.Run(ctx, serveAutoTick); err != nil {
		return fmt.Errorf("auto scheduler: %w", err)
	}
	return nil
}

func (a *serveAuto) tmpRoot() string { return filepath.Join(a.dataDir, "tmp") }

func (a *serveAuto) cleanup(ctx context.Context) error {
	root := a.tmpRoot()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("auto cleanup: %w", err)
	}
	if _, err := a.svc.Cleanup(ctx, auto.CleanupOptions{Root: root, Keep: 24 * time.Hour, Now: a.now()}); err != nil {
		return fmt.Errorf("auto cleanup: %w", err)
	}
	return nil
}

func (a *serveAuto) maintain(ctx context.Context) error {
	root := a.tmpRoot()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("auto maintenance: %w", err)
	}
	if _, err := a.svc.Maintain(ctx, auto.CleanupOptions{Root: root, Keep: 24 * time.Hour, Now: a.now()}); err != nil {
		return fmt.Errorf("auto maintenance: %w", err)
	}
	return nil
}

// maybeTitle fills the title of a session after its first completed turn. A
// disabled feature returns before the provider is touched; an existing title
// is never overwritten. The caller runs this off the turn path.
func (a *serveAuto) maybeTitle(ctx context.Context, prov loop.Provider, db *ctxdb.Store, sid, prompt, model string) error {
	if !a.svc.Enabled(auto.Title) {
		return nil
	}
	if _, ok, err := db.SessionLabel(ctx, sid, ctxdb.LabelTitle); err != nil {
		return fmt.Errorf("auto title label: %w", err)
	} else if ok {
		return nil
	}
	cfg := a.cfg
	cfg.Model = model
	svc, err := auto.New(cfg, &loopAutoClient{prov: prov, model: model}, auto.WithNow(a.now))
	if err != nil {
		return fmt.Errorf("auto title service: %w", err)
	}
	if _, err := auto.GenerateTitle(ctx, svc, autoTitleStore{db: db}, sid, prompt); err != nil {
		return fmt.Errorf("auto title: %w", err)
	}
	return nil
}

// autoTitleAfterTurn is the one-line serve hook used by runChat: the answer
// is already delivered, so title generation never delays the turn.
func (g *serveGateway) autoTitleAfterTurn(ctx context.Context, prov loop.Provider, sid, prompt string) {
	if g.auto != nil {
		g.auto.runTitle(ctx, prov, g.store.DB, sid, prompt, g.modelRef)
	}
}

// runTitle schedules the title call without blocking the turn.
func (a *serveAuto) runTitle(ctx context.Context, prov loop.Provider, db *ctxdb.Store, sid, prompt, model string) {
	if a == nil || !a.svc.Enabled(auto.Title) {
		return
	}
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	_ = boundary.Go(bg, "auto.title", func(ctx context.Context) error {
		defer cancel()
		return a.maybeTitle(ctx, prov, db, sid, prompt, model)
	})
}

// autoTitleStore adapts ctxdb labels to the title feature.
type autoTitleStore struct{ db *ctxdb.Store }

func (s autoTitleStore) SetTitle(ctx context.Context, sessionID, title string) error {
	if err := s.db.SetSessionLabel(ctx, sessionID, ctxdb.LabelTitle, title); err != nil {
		return fmt.Errorf("auto title: %w", err)
	}
	return nil
}

// loopAutoClient adapts the run's loop.Provider to the auto package's
// provider.Client seam. The purpose is passed through, so the production
// adapter bills this call as purpose=auto.
type loopAutoClient struct {
	prov  loop.Provider
	model string
}

func (c *loopAutoClient) Do(ctx context.Context, purpose provider.Purpose, req *provider.Request, onChunk func(provider.Chunk)) (provider.Response, error) {
	if c.prov == nil {
		return provider.Response{}, errors.New("auto: no provider")
	}
	lr := &loop.Request{Model: c.model, MaxTokens: req.MaxTokens}
	for i := range req.Messages {
		lr.Messages = append(lr.Messages, loop.Message{Role: req.Messages[i].Role, Content: req.Messages[i].Content})
	}
	reply, err := purposeStream(ctx, c.prov, purpose, lr)
	if err != nil {
		return provider.Response{}, err
	}
	if onChunk != nil && reply.Content != "" {
		onChunk(provider.Chunk{Content: []byte(reply.Content)})
	}
	resp := provider.Response{
		Content: reply.Content,
		Usage: budget.Usage{
			Input: reply.Usage.Input, CachedInput: reply.Usage.CachedInput,
			CacheCreation: reply.Usage.CacheCreation, Output: reply.Usage.Output,
			CostUSD: reply.Usage.CostUSD,
		},
		Model: reply.Model,
	}
	if p := reply.Usage.Price; p != nil {
		resp.Price = &budget.Price{InputPerMTok: p.InputPerMTok, CachedInputPerMTok: p.CachedInputPerMTok, OutputPerMTok: p.OutputPerMTok, Version: p.Version}
	}
	return resp, nil
}
