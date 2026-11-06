package raft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/netx"
)

// Config is the explicit activation of the local Raft wake-channel bridge.
type Config struct {
	Enabled    bool
	BaseURL    string
	Profile    string
	HTTPClient *http.Client
	Backoff    gwplat.Backoff
	Log        *slog.Logger
	Now        func() time.Time
}

// Adapter long-polls the local bridge and publishes replies to it.
type Adapter struct {
	cfg     Config
	desc    *gwplat.Descriptor
	out     *gwplat.Outbox
	handler gwplat.Handler
	dedupe  *gwplat.Dedupe
	log     *slog.Logger
	now     func() time.Time
	client  *http.Client
	cursor  int
}

// Errors of the adapter. Compared with errors.Is.
var (
	ErrConfig       = errors.New("raft: invalid config")
	ErrBridgeAbsent = errors.New("raft: local bridge is absent; install the Raft wake-channel bridge and set modules.gateway.raft.url")
	ErrStatus       = errors.New("raft: unexpected status")
)

// New validates an explicitly enabled, loopback-only bridge config.
func New(cfg *Config, desc *gwplat.Descriptor, out *gwplat.Outbox, h gwplat.Handler) (*Adapter, error) {
	if desc == nil || desc.Name != "raft" || cfg == nil {
		return nil, fmt.Errorf("%w: descriptor/config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("raft: activation: %w", err)
	}
	if err := validateBridge(cfg); err != nil {
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

// validateBridge requires a loopback base URL and a profile.
func validateBridge(cfg *Config) error {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Host == "" || base.User != nil {
		return fmt.Errorf("%w: base URL", ErrConfig)
	}
	host := base.Hostname()
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("%w: base URL must be loopback", ErrConfig)
	}
	if cfg.Profile == "" {
		return fmt.Errorf("%w: profile is required", ErrConfig)
	}
	return nil
}

// Outbox exposes the shared outbound seam to the wiring.
func (a *Adapter) Outbox() *gwplat.Outbox { return a.out }

// Run polls the bridge. A missing bridge is a permanent module_unavailable
// with installation guidance; transient failures back off.
func (a *Adapter) Run(ctx context.Context) error {
	for attempt := 1; ; attempt++ {
		err := a.poll(ctx)
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if errors.Is(err, ErrBridgeAbsent) {
			return fmt.Errorf("%w: %w", ErrBridgeAbsent, gwplat.Unavailablef("raft", "raft_bridge_absent"))
		}
		a.log.WarnContext(ctx, "raft poll failed", "attempt", attempt, "err", err)
		if waitErr := a.cfg.Backoff.Wait(ctx, attempt); waitErr != nil {
			return fmt.Errorf("raft: reconnect: %w", waitErr)
		}
	}
}

func (a *Adapter) poll(ctx context.Context) error {
	endpoint := strings.TrimRight(a.cfg.BaseURL, "/") + "/wake?profile=" + url.QueryEscape(a.cfg.Profile) +
		"&after=" + fmt.Sprint(a.cursor)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("raft: request: %w", err)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBridgeAbsent, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			return
		}
	}()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: wake %d", ErrStatus, resp.StatusCode)
	}
	var batch wakeBatch
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&batch); err != nil {
		return fmt.Errorf("raft: wake body: %w", err)
	}
	if batch.Next < a.cursor {
		return fmt.Errorf("%w: cursor regressed", ErrConfig)
	}
	a.cursor = batch.Next
	for i := range batch.Items {
		if err := a.dispatch(ctx, &batch.Items[i]); err != nil {
			return err
		}
	}
	return nil
}

type wakeItem struct {
	ID        string `json:"id"`
	Chat      string `json:"chat"`
	Sender    string `json:"sender"`
	Text      string `json:"text"`
	Group     bool   `json:"group"`
	Mentioned bool   `json:"mentioned"`
}

type wakeBatch struct {
	Items []wakeItem `json:"items"`
	Next  int        `json:"next"`
}

func (a *Adapter) dispatch(ctx context.Context, item *wakeItem) error {
	if item.ID == "" || item.Text == "" || !a.dedupe.Claim("raft:"+item.ID, a.now()) {
		return nil
	}
	in := &gateway.Inbound{
		Platform:  "raft",
		Account:   a.cfg.Profile,
		ChatID:    item.Chat,
		SenderID:  item.Sender,
		UpdateID:  "raft:" + item.ID,
		Text:      item.Text,
		Group:     item.Group,
		Mentioned: item.Mentioned,
	}
	if err := gwplat.Dispatch(ctx, a.handler, in); err != nil {
		return fmt.Errorf("raft: dispatch: %w", err)
	}
	return nil
}

// Publish posts one already-rendered reply to the bridge.
func (a *Adapter) Publish(ctx context.Context, chat, text string) error {
	payload, err := json.Marshal(map[string]string{"profile": a.cfg.Profile, "chat": chat, "text": text})
	if err != nil {
		return fmt.Errorf("raft: payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.cfg.BaseURL, "/")+"/send", strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("raft: request: %w", err)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("raft: publish: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			return
		}
	}()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%w: send %d", ErrStatus, resp.StatusCode)
	}
	return nil
}
