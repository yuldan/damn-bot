package signal

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
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/netx"
)

// Config is the explicit activation of the local signal-cli daemon adapter.
type Config struct {
	Enabled    bool
	BaseURL    string
	Account    string
	HTTPClient *http.Client
	Backoff    gwplat.Backoff
	Log        *slog.Logger
	Now        func() time.Time
}

// Adapter reads the daemon SSE stream and posts JSON-RPC sends. Message
// plaintext is never written to disk.
type Adapter struct {
	cfg     Config
	desc    *gwplat.Descriptor
	out     *gwplat.Outbox
	handler gwplat.Handler
	dedupe  *gwplat.Dedupe
	log     *slog.Logger
	now     func() time.Time
	client  *http.Client
	nextID  atomic.Int64
}

// Errors of the adapter. Compared with errors.Is.
var (
	ErrConfig = errors.New("signal: invalid config")
	ErrRPC    = errors.New("signal: rpc error")
)

// New validates an explicitly enabled, loopback-only daemon config.
func New(cfg *Config, desc *gwplat.Descriptor, out *gwplat.Outbox, h gwplat.Handler) (*Adapter, error) {
	if desc == nil || desc.Name != "signal" || cfg == nil {
		return nil, fmt.Errorf("%w: descriptor/config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("signal: activation: %w", err)
	}
	if err := validateDaemon(cfg); err != nil {
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

// validateDaemon requires a loopback daemon URL and a usable account id.
func validateDaemon(cfg *Config) error {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Host == "" || base.User != nil {
		return fmt.Errorf("%w: base URL", ErrConfig)
	}
	host := base.Hostname()
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("%w: base URL must be loopback", ErrConfig)
	}
	if _, ok := gwplat.SessionKey("signal", cfg.Account); !ok {
		return fmt.Errorf("%w: account", ErrConfig)
	}
	return nil
}

// Outbox exposes the shared outbound seam to the wiring.
func (a *Adapter) Outbox() *gwplat.Outbox { return a.out }

// Run holds the daemon event stream, reconnecting with capped backoff.
func (a *Adapter) Run(ctx context.Context) error {
	for attempt := 1; ; attempt++ {
		err := a.session(ctx)
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		a.log.WarnContext(ctx, "signal stream ended", "attempt", attempt, "err", err)
		if waitErr := a.cfg.Backoff.Wait(ctx, attempt); waitErr != nil {
			return fmt.Errorf("signal: reconnect: %w", waitErr)
		}
	}
}

func (a *Adapter) session(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(a.cfg.BaseURL, "/")+"/api/v1/events", nil)
	if err != nil {
		return fmt.Errorf("signal: request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("signal: subscribe: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			return
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: events %d", ErrRPC, resp.StatusCode)
	}
	parser := netx.NewSSEParser(resp.Body, 0)
	for {
		event, err := parser.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("signal: stream: %w", err)
		}
		a.handleEvent(ctx, event.Data)
	}
}

type rpcNotification struct {
	Method string `json:"method"`
	Params struct {
		Envelope envelope `json:"envelope"`
	} `json:"params"`
}

type envelope struct {
	Source      string `json:"source"`
	Timestamp   int64  `json:"timestamp"`
	DataMessage *struct {
		Message   string `json:"message"`
		Mentioned bool   `json:"mentioned"`
		GroupInfo *struct {
			GroupID string `json:"groupId"`
		} `json:"groupInfo"`
	} `json:"dataMessage"`
}

func (a *Adapter) handleEvent(ctx context.Context, data []byte) {
	var notification rpcNotification
	if err := json.Unmarshal(data, &notification); err != nil || notification.Method != "receive" {
		return
	}
	env := notification.Params.Envelope
	if env.Source == "" || env.Timestamp == 0 || env.DataMessage == nil || env.DataMessage.Message == "" {
		return
	}
	chat, group := env.Source, false
	if env.DataMessage.GroupInfo != nil && env.DataMessage.GroupInfo.GroupID != "" {
		chat, group = env.DataMessage.GroupInfo.GroupID, true
	}
	id := "signal:" + env.Source + "|" + strconv.FormatInt(env.Timestamp, 10) + "|" + chat
	if !a.dedupe.Claim(id, a.now()) {
		return
	}
	in := &gateway.Inbound{
		Platform:  "signal",
		Account:   a.cfg.Account,
		ChatID:    chat,
		SenderID:  env.Source,
		UpdateID:  id,
		Text:      env.DataMessage.Message,
		Group:     group,
		Mentioned: env.DataMessage.Mentioned,
	}
	if err := gwplat.Dispatch(ctx, a.handler, in); err != nil {
		a.log.WarnContext(ctx, "signal: dispatch failed", "err", err)
	}
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      string `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type rpcResponse struct {
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// Publish sends one already-rendered reply through the daemon JSON-RPC.
func (a *Adapter) Publish(ctx context.Context, chat, text string) error {
	id := strconv.FormatInt(a.nextID.Add(1), 10)
	payload, err := json.Marshal(rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "send",
		Params: map[string]any{
			"account":   a.cfg.Account,
			"recipient": []string{chat},
			"message":   text,
		},
	})
	if err != nil {
		return fmt.Errorf("signal: payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.cfg.BaseURL, "/")+"/api/v1/rpc", strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("signal: request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("signal: send: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			return
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: send %d", ErrRPC, resp.StatusCode)
	}
	var out rpcResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return fmt.Errorf("signal: response: %w", err)
	}
	if out.Error != nil {
		return fmt.Errorf("%w: %s", ErrRPC, out.Error.Message)
	}
	return nil
}
