// Package whatsapp_cloud is the official Meta WhatsApp Business Cloud API
// adapter (PLAT-026, GAP-GW-01). Inbound is a signed webhook: the verify
// token answers the GET handshake and `X-Hub-Signature-256` is an HMAC-SHA256
// of the raw body with the app secret. Outbound free-form text is legal only
// inside the 24-hour customer service window opened by an inbound message;
// a template is the only message allowed after it closes. Secrets come from
// the gwplat secret seam and never appear in an error or a log line.
package whatsapp_cloud

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
)

// Contract limits of the Cloud API surface.
const (
	// MaxWebhookBytes bounds one webhook body before signature checking.
	MaxWebhookBytes = 1 << 20
	// MaxMediaBytes is the conservative inbound ceiling; per-kind Meta limits
	// are tighter (see mediaLimit).
	MaxMediaBytes = 16 << 20
	// SignatureHeader carries the HMAC of the raw webhook body.
	SignatureHeader = "X-Hub-Signature-256"
)

// mediaLimit is the pinned Meta ceiling per media kind (build-time constants,
// never fetched). An unknown kind has no limit and is refused.
func mediaLimit(kind string) (int64, bool) {
	switch kind {
	case "image":
		return 5 << 20, true
	case "audio", "video", "document":
		return 16 << 20, true
	case "sticker":
		return 1 << 20, true
	}
	return 0, false
}

// Config is the explicit activation of the adapter. The zero value is
// disabled and dials nothing.
type Config struct {
	Enabled       bool
	PhoneNumberID string
	APIVersion    string
	// BaseURL overrides the Graph root; it must be https or loopback.
	BaseURL    string
	HTTPClient *http.Client
	Log        *slog.Logger
	Now        func() time.Time
}

// Adapter is the Cloud API webhook adapter: inbound is pushed by the gateway
// HTTP surface, and the shared outbox is the only outbound seam.
type Adapter struct {
	cfg  Config
	desc *gwplat.Descriptor
	out  *gwplat.Outbox
	h    gwplat.Handler
	log  *slog.Logger
	now  func() time.Time

	appSecret   string
	verifyToken string
	accessToken string
	client      *http.Client

	dedupe *gwplat.Dedupe
	mu     sync.Mutex
	// lastInbound is the 24-hour window state per chat id.
	lastInbound map[string]time.Time
}

// Errors of the adapter. Compared with errors.Is.
var (
	ErrConfig         = errors.New("whatsapp_cloud: invalid config")
	ErrSignature      = errors.New("whatsapp_cloud: invalid X-Hub-Signature-256")
	ErrVerify         = errors.New("whatsapp_cloud: webhook verification refused")
	ErrWebhookTooBig  = errors.New("whatsapp_cloud: webhook body too large")
	ErrMediaTooLarge  = errors.New("whatsapp_cloud: media exceeds the Cloud API limit")
	ErrInvalidPayload = errors.New("whatsapp_cloud: invalid webhook payload")
	ErrWindowClosed   = errors.New("whatsapp_cloud: session window closed; template required")
	ErrGraph          = errors.New("whatsapp_cloud: graph API error")
)

// New validates explicitly enabled config, loads the three declared secrets
// and returns a dormant adapter. It opens no connection.
func New(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, secrets gwplat.SecretSource, out *gwplat.Outbox, h gwplat.Handler) (*Adapter, error) {
	if desc == nil || desc.Name != "whatsapp_cloud" {
		return nil, fmt.Errorf("%w: descriptor is not whatsapp_cloud", ErrConfig)
	}
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("whatsapp_cloud: activation: %w", err)
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if err := validateBase(cfg); err != nil {
		return nil, err
	}
	if out == nil || h == nil {
		return nil, fmt.Errorf("%w: outbox and handler are required", ErrConfig)
	}
	values, err := gwplat.ResolveSecrets(ctx, desc, secrets)
	if err != nil {
		return nil, fmt.Errorf("whatsapp_cloud: activation: %w", err)
	}
	logger := cfg.Log
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	a := &Adapter{
		cfg: *cfg, desc: desc, out: out, h: h, log: logger, now: now,
		appSecret:   values["whatsapp_cloud_app_secret"],
		verifyToken: values["whatsapp_cloud_verify_token"],
		accessToken: values["whatsapp_cloud_access_token"],
		dedupe:      gwplat.NewDedupe(4096, 30*time.Minute),
		lastInbound: make(map[string]time.Time),
	}
	a.client = cfg.HTTPClient
	if a.client == nil {
		a.client = defaultClient(a.baseURL())
	}
	return a, nil
}

func validateConfig(cfg *Config) error {
	if strings.TrimSpace(cfg.PhoneNumberID) == "" || strings.ContainsAny(cfg.PhoneNumberID, "/\x00\r\n ") {
		return fmt.Errorf("%w: phone number id", ErrConfig)
	}
	return nil
}

// Outbox exposes the shared outbound seam to the wiring.
func (a *Adapter) Outbox() *gwplat.Outbox { return a.out }

// Verify answers the Meta GET handshake. The token is compared in constant
// time and only the challenge is returned.
func (a *Adapter) Verify(mode, token, challenge string) (string, error) {
	if mode != "subscribe" || !hmac.Equal([]byte(token), []byte(a.verifyToken)) {
		return "", ErrVerify
	}
	return challenge, nil
}

// HandleWebhook verifies and admits one webhook body. It returns the number of
// dispatched messages; a bad signature, an oversized body or an oversized
// media part dispatches nothing.
func (a *Adapter) HandleWebhook(ctx context.Context, header http.Header, body []byte) (int, error) {
	if len(body) > MaxWebhookBytes {
		return 0, ErrWebhookTooBig
	}
	if err := a.verifySignature(header.Get(SignatureHeader), body); err != nil {
		return 0, err
	}
	var payload webhook
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0, fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}
	dispatched := 0
	msgs := payload.messages()
	for i := range msgs {
		n, err := a.admit(ctx, &msgs[i])
		dispatched += n
		if err != nil {
			return dispatched, err
		}
	}
	return dispatched, nil
}

func (a *Adapter) verifySignature(header string, body []byte) error {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return ErrSignature
	}
	got, err := hex.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return ErrSignature
	}
	mac := hmac.New(sha256.New, []byte(a.appSecret))
	mac.Write(body)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return ErrSignature
	}
	return nil
}

// admit turns one Cloud API message into a gateway.Inbound. Media is bounded
// before anything is downloaded, and the id is deduped so a Meta retry never
// becomes a second turn.
func (a *Adapter) admit(ctx context.Context, msg *inboundMessage) (int, error) {
	if !a.dedupe.Claim("wa:"+msg.ID, a.now()) {
		return 0, nil
	}
	media, err := msg.mediaParts()
	if err != nil {
		return 0, err
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}
	a.mu.Lock()
	a.lastInbound[msg.From] = msg.sentAt(a.now())
	a.mu.Unlock()
	text, command := msg.text()
	in := &gateway.Inbound{
		Platform:  a.desc.Name,
		Account:   a.cfg.PhoneNumberID,
		ChatID:    msg.From,
		SenderID:  msg.From,
		Text:      text,
		UpdateID:  msg.ID,
		Payload:   string(raw),
		Group:     strings.HasSuffix(msg.From, "@g.us"),
		Command:   command,
		Mentioned: false,
	}
	if len(media) > 0 {
		in.Payload, err = withMedia(in.Payload, media)
		if err != nil {
			return 0, err
		}
	}
	if err := gwplat.Dispatch(ctx, a.h, in); err != nil {
		return 0, fmt.Errorf("whatsapp_cloud: dispatch: %w", err)
	}
	return 1, nil
}

func withMedia(payload string, media []MediaPart) (string, error) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(payload), &doc); err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}
	doc["gw_media"] = media
	out, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}
	return string(out), nil
}

// MediaPart is the bounded inbound media reference handed to the pipeline.
type MediaPart struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Mime     string `json:"mime"`
	FileSize int64  `json:"file_size"`
	Caption  string `json:"caption,omitempty"`
	Filename string `json:"filename,omitempty"`
}

// webhook is the subset of the Meta payload the adapter consumes.
type webhook struct {
	Object string `json:"object"`
	Entry  []struct {
		Changes []struct {
			Field string `json:"field"`
			Value struct {
				Messages []inboundMessage `json:"messages"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

func (w *webhook) messages() []inboundMessage {
	var out []inboundMessage
	for _, entry := range w.Entry {
		for _, change := range entry.Changes {
			out = append(out, change.Value.Messages...)
		}
	}
	return out
}

type mediaRef struct {
	ID       string `json:"id"`
	MimeType string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
	Caption  string `json:"caption"`
	Filename string `json:"filename"`
}

type inboundMessage struct {
	From      string `json:"from"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Text      *struct {
		Body string `json:"body"`
	} `json:"text"`
	Button      *struct{ Text string } `json:"button"`
	Interactive *struct {
		ButtonReply *struct{ ID, Title string } `json:"button_reply"`
		ListReply   *struct{ ID, Title string } `json:"list_reply"`
	} `json:"interactive"`
	Image    *mediaRef `json:"image"`
	Audio    *mediaRef `json:"audio"`
	Video    *mediaRef `json:"video"`
	Document *mediaRef `json:"document"`
	Sticker  *mediaRef `json:"sticker"`
}

func (m *inboundMessage) text() (string, bool) {
	var text string
	switch {
	case m.Text != nil:
		text = m.Text.Body
	case m.Button != nil:
		text = m.Button.Text
	case m.Interactive != nil && m.Interactive.ButtonReply != nil:
		text = m.Interactive.ButtonReply.Title
	case m.Interactive != nil && m.Interactive.ListReply != nil:
		text = m.Interactive.ListReply.Title
	}
	return text, strings.HasPrefix(strings.TrimSpace(text), "/")
}

func (m *inboundMessage) sentAt(fallback time.Time) time.Time {
	seconds, err := strconv.ParseInt(m.Timestamp, 10, 64)
	if err != nil || seconds < 0 {
		return fallback
	}
	return time.Unix(seconds, 0)
}

func (m *inboundMessage) mediaParts() ([]MediaPart, error) {
	var parts []MediaPart
	for _, f := range []struct {
		kind string
		ref  *mediaRef
	}{
		{"image", m.Image},
		{"audio", m.Audio},
		{"video", m.Video},
		{"document", m.Document},
		{"sticker", m.Sticker},
	} {
		if f.ref == nil {
			continue
		}
		limit, ok := mediaLimit(f.kind)
		if !ok || f.ref.FileSize > limit || f.ref.FileSize > MaxMediaBytes {
			return nil, fmt.Errorf("%w: %s %d bytes", ErrMediaTooLarge, f.kind, f.ref.FileSize)
		}
		parts = append(parts, MediaPart{
			ID: f.ref.ID, Kind: f.kind, Mime: f.ref.MimeType,
			FileSize: f.ref.FileSize, Caption: f.ref.Caption, Filename: f.ref.Filename,
		})
	}
	return parts, nil
}
