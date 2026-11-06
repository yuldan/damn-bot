// Package photon implements the PLAT-014 adapter: iMessage through the Photon
// Spectrum sidecar over loopback HTTP. The sidecar is a separate executable; a
// missing socket is module_unavailable{photon_sidecar_absent} before any dial.
package photon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/netx"
)

// ErrConfig rejects an unusable activation.
var ErrConfig = errors.New("photon: invalid config")

// ErrSidecarAbsent means the local sidecar is not installed or not running.
var ErrSidecarAbsent = errors.New("photon: sidecar is absent")

// Config is the explicit activation of the adapter; the zero value is dormant.
type Config struct {
	Enabled    bool
	BaseURL    string
	SocketPath string
	ProjectID  string
	HTTPClient *http.Client
	// Present is the sidecar probe; the default stats SocketPath and never
	// dials. A test injects its own probe.
	Present func() error
	Backoff gwplat.Backoff
	Log     *slog.Logger
	Now     func() time.Time
}

// Adapter polls the sidecar for inbound iMessages and posts replies.
type Adapter struct {
	cfg     Config
	out     *gwplat.Outbox
	handler gwplat.Handler
	token   string
	dedupe  *gwplat.Dedupe
	log     *slog.Logger
	now     func() time.Time
	client  *http.Client
}

// New validates the explicit config, probes the sidecar without dialing, and
// loads the project secret from the secret seam only.
func New(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, secrets gwplat.SecretSource, out *gwplat.Outbox, h gwplat.Handler) (*Adapter, error) {
	if desc == nil || desc.Name != "photon" || cfg == nil {
		return nil, fmt.Errorf("%w: descriptor/config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("photon: activation: %w", err)
	}
	if out == nil || h == nil {
		return nil, fmt.Errorf("%w: outbox and handler are required", ErrConfig)
	}
	if err := probe(cfg); err != nil {
		return nil, fmt.Errorf("photon: %w", err)
	}
	values, err := gwplat.ResolveSecrets(ctx, desc, secrets)
	if err != nil {
		return nil, fmt.Errorf("photon: secret: %w", err)
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
		if cfg.SocketPath != "" {
			return nil, fmt.Errorf("%w: HTTP client is required for a unix sidecar (E-05)", ErrConfig)
		}
		client = netx.LoopbackClient()
	}
	return &Adapter{
		cfg: *cfg, out: out, handler: h, token: values["photon_project_secret"],
		dedupe: gwplat.NewDedupe(4096, 24*time.Hour), log: logger, now: now, client: client,
	}, nil
}

// probe is the zero-network presence check: the sidecar socket must exist.
func probe(cfg *Config) error {
	if cfg.Present != nil {
		if err := cfg.Present(); err != nil {
			return fmt.Errorf("%w: %w", ErrSidecarAbsent, gwplat.Unavailablef("photon", "photon_sidecar_absent"))
		}
		return nil
	}
	if strings.TrimSpace(cfg.ProjectID) == "" {
		return fmt.Errorf("%w: project id", ErrConfig)
	}
	if cfg.SocketPath == "" {
		if err := validateLoopback(cfg.BaseURL); err != nil {
			return err
		}
		return nil
	}
	if _, err := os.Stat(cfg.SocketPath); err != nil {
		return fmt.Errorf("%w: %w", ErrSidecarAbsent, gwplat.Unavailablef("photon", "photon_sidecar_absent"))
	}
	return nil
}

func validateLoopback(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return fmt.Errorf("%w: sidecar URL", ErrConfig)
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if u.Scheme != "http" || (!strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback())) {
		return fmt.Errorf("%w: sidecar URL must be loopback http", ErrConfig)
	}
	return nil
}

// Outbox exposes the shared outbound seam to the wiring.
func (a *Adapter) Outbox() *gwplat.Outbox { return a.out }

// Run polls the sidecar; the adapter never reconnects to a missing sidecar.
func (a *Adapter) Run(ctx context.Context) error {
	backoff := gwplat.Backoff{}
	for attempt := 1; ; attempt++ {
		err := a.poll(ctx)
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if errors.Is(err, ErrSidecarAbsent) {
			return err
		}
		a.log.WarnContext(ctx, "photon poll failed", "attempt", attempt, "err", err)
		if waitErr := backoff.Wait(ctx, attempt); waitErr != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
			}
			return fmt.Errorf("photon: reconnect: %w", waitErr)
		}
	}
}

type message struct {
	ID      string `json:"id"`
	Chat    string `json:"chat"`
	From    string `json:"from"`
	Text    string `json:"text"`
	Group   bool   `json:"group"`
	Mention bool   `json:"mentioned"`
}

func (a *Adapter) poll(ctx context.Context) error {
	var batch struct {
		Messages []message `json:"messages"`
	}
	if err := a.do(ctx, http.MethodGet, "/photon/v1/messages?project="+url.QueryEscape(a.cfg.ProjectID), nil, &batch); err != nil {
		return err
	}
	for i := range batch.Messages {
		if err := a.dispatch(ctx, &batch.Messages[i]); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) dispatch(ctx context.Context, msg *message) error {
	if msg.ID == "" || strings.TrimSpace(msg.Text) == "" || !a.dedupe.Claim("photon:"+msg.ID, a.now()) {
		return nil
	}
	in := &gateway.Inbound{
		Platform: "photon", Account: a.cfg.ProjectID, ChatID: msg.Chat, SenderID: msg.From,
		UpdateID: "photon:" + msg.ID, Text: msg.Text, Group: msg.Group, Mentioned: msg.Mention,
	}
	if err := gwplat.Dispatch(ctx, a.handler, in); err != nil {
		return fmt.Errorf("photon: dispatch: %w", err)
	}
	return nil
}

// Send renders and posts one reply through the sidecar.
func (a *Adapter) Send(ctx context.Context, chat, markdown string) error {
	if err := a.out.Reply(ctx, gwplat.Reply{Chat: chat, Markdown: markdown, Send: a.sendText(chat)}); err != nil {
		return fmt.Errorf("photon: reply: %w", err)
	}
	return nil
}

func (a *Adapter) sendText(chat string) gwplat.SendFunc {
	return func(ctx context.Context, text string) error {
		body := map[string]string{"chat": chat, "text": text}
		var out map[string]string
		return a.do(ctx, http.MethodPost, "/photon/v1/send", body, &out)
	}
}

func (a *Adapter) do(ctx context.Context, method, path string, body, out any) error {
	base := strings.TrimRight(a.cfg.BaseURL, "/")
	if base == "" {
		base = "http://unix"
	}
	var reader *strings.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("photon: encode: %w", err)
		}
		reader = strings.NewReader(string(raw))
	} else {
		reader = strings.NewReader("")
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, reader)
	if err != nil {
		return fmt.Errorf("photon: request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrSidecarAbsent, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			return
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("photon: %s %s: status %d", method, path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("photon: decode: %w", err)
	}
	return nil
}
