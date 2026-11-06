package ntfy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/netx"
)

// Config is the explicit activation of the ntfy adapter. The zero value is
// disabled and opens no connection.
type Config struct {
	Enabled    bool
	BaseURL    string
	Topic      string
	HTTPClient *http.Client
	Backoff    gwplat.Backoff
	Log        *slog.Logger
	Now        func() time.Time
}

// Adapter subscribes one ntfy topic over SSE and publishes replies to it.
type Adapter struct {
	cfg     Config
	desc    *gwplat.Descriptor
	out     *gwplat.Outbox
	handler gwplat.Handler
	dedupe  *gwplat.Dedupe
	log     *slog.Logger
	now     func() time.Time
	client  *http.Client
}

// Errors of the adapter. Compared with errors.Is.
var (
	ErrConfig = errors.New("ntfy: invalid config")
	ErrStatus = errors.New("ntfy: unexpected status")
)

// New validates explicitly enabled config and returns a dormant adapter.
func New(cfg *Config, desc *gwplat.Descriptor, out *gwplat.Outbox, h gwplat.Handler) (*Adapter, error) {
	if desc == nil || desc.Name != "ntfy" || cfg == nil {
		return nil, fmt.Errorf("%w: descriptor/config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("ntfy: activation: %w", err)
	}
	if err := validateTarget(cfg); err != nil {
		return nil, err
	}
	if out == nil || h == nil {
		return nil, fmt.Errorf("%w: outbox and handler are required", ErrConfig)
	}
	logger := cfg.Log
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	client := cfg.HTTPClient
	if client == nil {
		client = netx.LoopbackClient()
	}
	return &Adapter{
		cfg: *cfg, desc: desc, out: out, handler: h,
		dedupe: gwplat.NewDedupe(4096, 30*time.Minute),
		log:    logger, now: now, client: client,
	}, nil
}

// validateTarget checks the base URL and topic before any connection.
func validateTarget(cfg *Config) error {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Host == "" || (base.Scheme != "https" && base.Scheme != "http") {
		return fmt.Errorf("%w: base URL", ErrConfig)
	}
	if base.User != nil || cfg.Topic == "" || strings.ContainsAny(cfg.Topic, " \x00\r\n/") {
		return fmt.Errorf("%w: topic/base URL", ErrConfig)
	}
	return nil
}

// Outbox exposes the shared outbound seam to the wiring.
func (a *Adapter) Outbox() *gwplat.Outbox { return a.out }

// Run keeps one SSE subscription alive with capped backoff.
func (a *Adapter) Run(ctx context.Context) error {
	for attempt := 1; ; attempt++ {
		err := a.session(ctx)
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		a.log.WarnContext(ctx, "ntfy stream ended", "attempt", attempt, "err", err)
		if waitErr := a.cfg.Backoff.Wait(ctx, attempt); waitErr != nil {
			return fmt.Errorf("ntfy: reconnect: %w", waitErr)
		}
	}
}

func (a *Adapter) session(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.streamURL(), nil)
	if err != nil {
		return fmt.Errorf("ntfy: request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy: subscribe: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			return
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: subscribe %d", ErrStatus, resp.StatusCode)
	}
	parser := netx.NewSSEParser(resp.Body, 0)
	for {
		event, err := parser.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("ntfy: stream: %w", err)
		}
		a.handleEvent(ctx, event)
	}
}

// handleEvent dispatches one ntfy JSON message; content is never logged at
// INFO.
func (a *Adapter) handleEvent(ctx context.Context, event netx.Event) {
	msg, ok := parseMessage(event.Data)
	if !ok {
		return
	}
	id := msg.ID
	if id == "" {
		id = msg.Time
	}
	if !a.dedupe.Claim("ntfy:"+id, a.now()) {
		return
	}
	in := &gateway.Inbound{
		Platform: "ntfy",
		Account:  a.cfg.Topic,
		ChatID:   a.cfg.Topic,
		SenderID: "ntfy",
		UpdateID: "ntfy:" + id,
		Text:     msg.Message,
	}
	if err := gwplat.Dispatch(ctx, a.handler, in); err != nil {
		a.log.WarnContext(ctx, "ntfy: dispatch failed", "err", err)
	}
}

// Publish posts one already-rendered reply to the topic.
func (a *Adapter) Publish(ctx context.Context, text string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.topicURL(), strings.NewReader(text))
	if err != nil {
		return fmt.Errorf("ntfy: request: %w", err)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("ntfy: publish: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			return
		}
	}()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%w: publish %d", ErrStatus, resp.StatusCode)
	}
	return nil
}

func (a *Adapter) topicURL() string { return strings.TrimRight(a.cfg.BaseURL, "/") + "/" + a.cfg.Topic }

func (a *Adapter) streamURL() string {
	return a.topicURL() + "/sse"
}
