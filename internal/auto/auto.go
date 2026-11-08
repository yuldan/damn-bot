//go:build !minimal

package auto

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/provider"
)

// Feature is one auxiliary automation key. The set is closed: an unknown
// key is rejected by [New] and by the config decoder (ADR-333).
type Feature string

// The twelve parity features (docs/FEATURE-PARITY.md AUTO-001..032).
const (
	Review          Feature = "review"       // AUTO-001
	Title           Feature = "title"        // AUTO-002
	Curator         Feature = "curator"      // AUTO-003
	Compaction      Feature = "compaction"   // AUTO-004
	Blueprints      Feature = "blueprints"   // AUTO-012
	Suggestions     Feature = "suggestions"  // AUTO-013
	Goals           Feature = "goals"        // AUTO-014
	Loops           Feature = "loops"        // AUTO-015
	Heartbeat       Feature = "heartbeat"    // AUTO-016
	DiskCleanup     Feature = "disk_cleanup" // AUTO-030
	Maintenance     Feature = "maintenance"  // AUTO-031
	OutboundWebhook Feature = "webhooks"     // AUTO-032
)

// Definition is the immutable registry row of one feature.
type Definition struct {
	ID      string // parity row, e.g. "AUTO-001"
	Feature Feature
	// DefaultDailyTokens is the SPEC §9.11 budget when the section is
	// enabled without an explicit daily_tokens.
	DefaultDailyTokens int64
	// LLM reports whether the feature may call the provider. Non-LLM
	// features still carry a small budget so config validation has one
	// rule for every key.
	LLM bool
	// Trigger is the human-readable condition from SPEC §9.11.
	Trigger string
}

func definitions() []Definition {
	return []Definition{
		{"AUTO-001", Review, 50000, true, "turn completed after >= 8 iterations, at most once per 10 turns"},
		{"AUTO-002", Title, 2000, true, "first completed turn of a session"},
		{"AUTO-003", Curator, 30000, true, "daily in serve while the session is idle (>= 2 h)"},
		{"AUTO-004", Compaction, 20000, true, "gateway session idle or too long"},
		{"AUTO-012", Blueprints, 4000, true, "explicit /blueprint invocation"},
		{"AUTO-013", Suggestions, 4000, true, "explicit /suggestions invocation"},
		{"AUTO-014", Goals, 20000, true, "standing goal judge, max 20 turns"},
		{"AUTO-015", Loops, 4000, false, "explicit /loop interval prompt, min 30 s"},
		{"AUTO-016", Heartbeat, 4000, false, "explicit /heartbeat while the session is idle"},
		{"AUTO-030", DiskCleanup, 1000, false, "explicit cleanup or scheduled maintenance"},
		{"AUTO-031", Maintenance, 1000, false, "CLI start with an explicit opt-in"},
		{"AUTO-032", OutboundWebhook, 1000, false, "event delivery to a configured webhook"},
	}
}

// ErrDisabled means the feature section is off; no provider call is made.
var ErrDisabled = errors.New("auto: feature disabled")

// ErrBudget means the feature exhausted its daily token or USD budget.
var ErrBudget = errors.New("auto: budget exhausted")

// ErrUnknownFeature rejects a key outside the closed registry.
var ErrUnknownFeature = errors.New("auto: unknown feature")

// ErrConfig rejects a malformed feature configuration.
var ErrConfig = errors.New("auto: invalid config")

// ErrNoClient means an enabled feature tried to call without a provider.
var ErrNoClient = errors.New("auto: no provider client")

// Definitions returns a copy of the closed registry in parity order.
func Definitions() []Definition { return slices.Clone(definitions()) }

// Lookup returns one registry row.
func Lookup(f Feature) (Definition, bool) {
	for _, d := range definitions() {
		if d.Feature == f {
			return d, true
		}
	}
	return Definition{}, false
}

// Limits is the effective budget of one feature. DailyUSDMicros 0 means no
// separate USD cap; DailyTokens is always positive for an enabled feature.
type Limits struct {
	Enabled        bool
	DailyTokens    int64
	DailyUSDMicros int64
}

// Config is the validated runtime view of [modules.auto.<feature>].
type Config struct {
	Model    string
	Features map[Feature]Limits
	// MaxOutput is the per-call output ceiling; zero uses
	// provider.DefaultMaxTokens.
	MaxOutput int
}

// Validate fills default token budgets for absent entries and rejects an
// unknown key or an enabled feature without any cap.
func (c *Config) Validate() error {
	if c.Features == nil {
		c.Features = map[Feature]Limits{}
	}
	for f, l := range c.Features {
		d, ok := Lookup(f)
		if !ok {
			return fmt.Errorf("%w: %q", ErrUnknownFeature, f)
		}
		if l.DailyTokens < 0 || l.DailyUSDMicros < 0 {
			return fmt.Errorf("%w: %s has a negative budget", ErrConfig, f)
		}
		if l.DailyTokens == 0 {
			l.DailyTokens = d.DefaultDailyTokens
			c.Features[f] = l
		}
	}
	if c.MaxOutput < 0 {
		return fmt.Errorf("%w: max_output must be >= 0", ErrConfig)
	}
	return nil
}

// Event is one auxiliary decision or call, ready for the llm_call ledger.
type Event struct {
	Feature      Feature
	Purpose      provider.Purpose
	PromptTokens int64 // estimated request tokens at the call start
	InputTokens  int64
	OutputTokens int64
	CostMicros   *int64
	Called       bool
	Denied       string // ErrDisabled/ErrBudget reason when Called is false
}

// Service is the single auxiliary call path. It is safe for concurrent use.
type Service struct {
	cfg    Config
	client provider.Client
	now    func() time.Time

	mu    sync.Mutex
	day   string
	spent map[Feature]spend
	sink  func(Event)
}

type spend struct {
	tokens     int64
	micros     int64
	usdUnknown bool
}

// Option customises a Service (tests inject the clock).
type Option func(*Service)

// WithNow overrides the UTC clock used for the daily budget window.
func WithNow(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// New validates cfg and binds the provider client. A nil client is allowed
// while every feature is off; Call fails closed with [ErrNoClient].
func New(cfg Config, client provider.Client, opts ...Option) (*Service, error) {
	cfg.Features = maps.Clone(cfg.Features)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	s := &Service{
		cfg:    cfg,
		client: client,
		now:    time.Now,
		spent:  map[Feature]spend{},
		day:    time.Now().UTC().Format("2006-01-02"),
	}
	for _, opt := range opts {
		opt(s)
	}
	s.day = s.now().UTC().Format("2006-01-02")
	return s, nil
}

// SetEventSink installs the llm_call recorder. The sink is called without
// the service lock held.
func (s *Service) SetEventSink(fn func(Event)) {
	s.mu.Lock()
	s.sink = fn
	s.mu.Unlock()
}

// Enabled reports whether the feature section is on.
func (s *Service) Enabled(f Feature) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Features[f].Enabled
}

// Spent returns the tokens and micro-USD charged to f in the current UTC day.
func (s *Service) Spent(f Feature) (tokens, micros int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollDayLocked()
	x := s.spent[f]
	return x.tokens, x.micros
}

// Call runs one auxiliary provider request for f. Disabled features and an
// exhausted budget return before the provider is touched; every allowed
// request carries purpose=auto and clamps output tokens to the remaining
// token budget, so a feature stops at its limit instead of crossing it by a
// full call.
func (s *Service) Call(ctx context.Context, f Feature, prompt string) (provider.Response, error) {
	_, ok := Lookup(f)
	if !ok {
		return provider.Response{}, fmt.Errorf("%w: %q", ErrUnknownFeature, f)
	}
	s.mu.Lock()
	s.rollDayLocked()
	lim := s.cfg.Features[f]
	if !lim.Enabled {
		fn := s.sink
		s.mu.Unlock()
		notify(fn, &Event{Feature: f, Called: false, Denied: ErrDisabled.Error()})
		return provider.Response{}, fmt.Errorf("%w: %s", ErrDisabled, f)
	}
	remaining := lim.DailyTokens - s.spent[f].tokens
	if remaining <= 0 || (lim.DailyUSDMicros > 0 && (s.spent[f].micros >= lim.DailyUSDMicros || s.spent[f].usdUnknown)) {
		fn := s.sink
		s.mu.Unlock()
		notify(fn, &Event{Feature: f, Called: false, Denied: ErrBudget.Error()})
		return provider.Response{}, fmt.Errorf("%w: %s", ErrBudget, f)
	}
	if s.client == nil {
		fn := s.sink
		s.mu.Unlock()
		notify(fn, &Event{Feature: f, Called: false, Denied: ErrNoClient.Error()})
		return provider.Response{}, fmt.Errorf("%w: %s", ErrNoClient, f)
	}
	maxOut := s.cfg.MaxOutput
	if maxOut <= 0 {
		maxOut = provider.DefaultMaxTokens
	}
	if int64(maxOut) > remaining {
		maxOut = int(remaining)
	}
	req := &provider.Request{
		Model:     s.cfg.Model,
		Messages:  []provider.Message{{Role: "user", Content: prompt}},
		MaxTokens: maxOut,
		Origin:    "auto",
	}
	est := budget.Count(prompt).Raw()
	s.mu.Unlock()

	resp, err := s.client.Do(ctx, provider.PurposeAuto, req, nil)
	if err != nil {
		// A failed attempt may still be billed; charge only what the
		// provider reported and never retry here (non-critical lane).
		s.emit(&Event{Feature: f, Purpose: provider.PurposeAuto, PromptTokens: est, Called: true})
		return resp, fmt.Errorf("auto: %s provider: %w", f, err)
	}
	micros, costErr := tax(&resp)
	s.charge(f, resp.Usage.Input+resp.Usage.Output, micros)
	s.emit(&Event{
		Feature: f, Purpose: provider.PurposeAuto, PromptTokens: est,
		InputTokens: resp.Usage.Input, OutputTokens: resp.Usage.Output,
		CostMicros: micros, Called: true,
	})
	if costErr != nil {
		return resp, fmt.Errorf("auto: %s usage: %w", f, costErr)
	}
	return resp, nil
}

// tax prices one response; an unknown cost is nil unless the feature has a
// USD cap, in which case the caller fails closed for the rest of the day.
func tax(resp *provider.Response) (*int64, error) {
	micros, _, err := budget.Cost(resp.Usage, resp.Price)
	if err != nil {
		return nil, fmt.Errorf("auto: cost: %w", err)
	}
	return micros, nil
}

func (s *Service) charge(f Feature, tokens int64, micros *int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rollDayLocked()
	x := s.spent[f]
	x.tokens += tokens
	if micros != nil {
		x.micros += *micros
	} else if s.cfg.Features[f].DailyUSDMicros > 0 {
		x.usdUnknown = true
	}
	s.spent[f] = x
}

func (s *Service) rollDayLocked() {
	day := s.now().UTC().Format("2006-01-02")
	if day != s.day {
		s.day = day
		clear(s.spent)
	}
}

func (s *Service) emit(ev *Event) {
	s.mu.Lock()
	fn := s.sink
	s.mu.Unlock()
	if fn != nil {
		fn(*ev)
	}
}

func notify(fn func(Event), ev *Event) {
	if fn != nil {
		fn(*ev)
	}
}
