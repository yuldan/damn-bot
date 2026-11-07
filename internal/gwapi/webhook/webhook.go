package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
)

// Contract limits and header names of the generic webhook surface.
const (
	// MaxBodyBytes bounds one webhook body before it is parsed.
	MaxBodyBytes = 1 << 20
	// MaxTextBytes bounds the message text carried in one envelope.
	MaxTextBytes = 64 << 10
	// MaxIDBytes bounds a delivery id, event name, chat and sender id.
	MaxIDBytes = 128
	// SignatureHeader carries the HMAC-SHA256 of the raw body:
	// `sha256=<hex>`.
	SignatureHeader = "X-Webhook-Signature"
	// DefaultDedupeSize and DefaultDedupeTTL bound the replay window.
	DefaultDedupeSize = 4096
	DefaultDedupeTTL  = 30 * time.Minute
)

// Errors of the receiver. Compared with errors.Is.
var (
	ErrConfig         = errors.New("webhook: invalid config")
	ErrSignature      = errors.New("webhook: invalid X-Webhook-Signature")
	ErrTooLarge       = errors.New("webhook: body exceeds the receiver limit")
	ErrInvalidPayload = errors.New("webhook: invalid envelope")
)

var (
	eventName = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,63}$`)
	idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,127}$`)
)

// Config is the explicit activation of the receiver. The zero value is
// disabled and accepts nothing.
type Config struct {
	Enabled      bool
	MaxBodyBytes int64
	Log          *slog.Logger
	Now          func() time.Time
}

// Adapter is the generic webhook receiver. Inbound is pushed by an HTTP
// surface; the shared outbox is the only outbound seam.
type Adapter struct {
	cfg    Config
	desc   *gwplat.Descriptor
	out    *gwplat.Outbox
	h      gwplat.Handler
	log    *slog.Logger
	now    func() time.Time
	secret []byte
	dedupe *gwplat.Dedupe
}

// New validates the explicitly enabled config, loads the shared secret and
// returns a receiver. It opens no connection and starts no goroutine.
func New(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, secrets gwplat.SecretSource,
	out *gwplat.Outbox, h gwplat.Handler,
) (*Adapter, error) {
	if desc == nil || desc.Name != "webhook" {
		return nil, fmt.Errorf("%w: descriptor is not webhook", ErrConfig)
	}
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("webhook: activation: %w", err)
	}
	if h == nil {
		return nil, fmt.Errorf("%w: handler is required", ErrConfig)
	}
	values, err := gwplat.ResolveSecrets(ctx, desc, secrets)
	if err != nil {
		return nil, fmt.Errorf("webhook: activation: %w", err)
	}
	logger := cfg.Log
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	maxBody := cfg.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = MaxBodyBytes
	}
	a := &Adapter{
		cfg:    *cfg,
		desc:   desc,
		out:    out,
		h:      h,
		log:    logger,
		now:    now,
		secret: []byte(values["webhook_secret"]),
		dedupe: gwplat.NewDedupe(DefaultDedupeSize, DefaultDedupeTTL),
	}
	a.cfg.MaxBodyBytes = maxBody
	return a, nil
}

// Descriptor returns the adapter contract row.
func (a *Adapter) Descriptor() gwplat.Descriptor { return *a.desc }

// Outbox exposes the shared outbound seam to the wiring. A receive-only
// receiver (HTTP ACK, no reply transport) holds a nil outbox.
func (a *Adapter) Outbox() *gwplat.Outbox { return a.out }

// Envelope is the wire form a sender posts. A retry repeats the same id, so
// the replay window turns it into one turn.
type Envelope struct {
	ID        string `json:"id"`
	Event     string `json:"event"`
	Chat      string `json:"chat"`
	Thread    string `json:"thread,omitempty"`
	Sender    string `json:"sender"`
	Text      string `json:"text"`
	Group     bool   `json:"group,omitempty"`
	Mentioned bool   `json:"mentioned,omitempty"`
}

// Sign returns the header value for body with secret.
func Sign(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func (a *Adapter) verify(header http.Header, body []byte) error {
	if int64(len(body)) > a.cfg.MaxBodyBytes {
		return ErrTooLarge
	}
	got, ok := strings.CutPrefix(header.Get(SignatureHeader), "sha256=")
	if !ok {
		return ErrSignature
	}
	sum, err := hex.DecodeString(got)
	if err != nil {
		return ErrSignature
	}
	mac := hmac.New(sha256.New, a.secret)
	mac.Write(body)
	if !hmac.Equal(sum, mac.Sum(nil)) {
		return ErrSignature
	}
	return nil
}

// ValidID reports whether value is a safe delivery/sender/chat id.
func ValidID(value string) bool { return idPattern.MatchString(value) }

func (e *Envelope) validate() error {
	if !ValidID(e.ID) || !ValidID(e.Chat) || !ValidID(e.Sender) {
		return fmt.Errorf("%w: id, chat or sender", ErrInvalidPayload)
	}
	if e.Thread != "" && !ValidID(e.Thread) {
		return fmt.Errorf("%w: thread", ErrInvalidPayload)
	}
	if !eventName.MatchString(e.Event) {
		return fmt.Errorf("%w: event", ErrInvalidPayload)
	}
	if len(e.Text) > MaxTextBytes {
		return fmt.Errorf("%w: text", ErrInvalidPayload)
	}
	return nil
}

// Admit verifies and decodes one delivery without dispatching it. It is the
// part a custom HTTP surface needs when the caller owns retries.
func (a *Adapter) Admit(header http.Header, body []byte) (*gateway.Inbound, error) {
	if err := a.verify(header, body); err != nil {
		return nil, err
	}
	var env Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}
	if err := env.validate(); err != nil {
		return nil, err
	}
	return &gateway.Inbound{
		Platform:  a.desc.Name,
		ChatID:    env.Chat,
		ThreadID:  env.Thread,
		SenderID:  env.Sender,
		Text:      env.Text,
		UpdateID:  "webhook:" + env.ID,
		Payload:   string(body),
		Group:     env.Group,
		Mentioned: env.Mentioned,
	}, nil
}

// Handle verifies, dedupes and dispatches one delivery. A replayed id returns
// zero dispatches; a bad signature or oversized body never reaches the handler.
func (a *Adapter) Handle(ctx context.Context, header http.Header, body []byte) (int, error) {
	in, err := a.Admit(header, body)
	if err != nil {
		return 0, err
	}
	if !a.dedupe.Claim(in.UpdateID, a.now()) {
		return 0, nil
	}
	if err := gwplat.Dispatch(ctx, a.h, in); err != nil {
		return 0, fmt.Errorf("webhook: dispatch: %w", err)
	}
	return 1, nil
}

// ServeHTTP is the receiver endpoint: POST only, bounded body, constant-time
// signature, dedupe, then dispatch. The payload never enters a log line.
func (a *Adapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, a.cfg.MaxBodyBytes+1))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	if int64(len(body)) > a.cfg.MaxBodyBytes {
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
		return
	}
	n, err := a.Handle(r.Context(), r.Header, body)
	if err != nil {
		a.reject(w, err)
		return
	}
	a.log.DebugContext(r.Context(), "webhook delivery", "dispatched", n)
	w.WriteHeader(http.StatusOK)
	if _, werr := io.WriteString(w, "ok"); werr != nil {
		return
	}
}

func (a *Adapter) reject(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrTooLarge):
		http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
	case errors.Is(err, ErrSignature):
		http.Error(w, "invalid signature", http.StatusUnauthorized)
	case errors.Is(err, ErrInvalidPayload):
		http.Error(w, "invalid payload", http.StatusBadRequest)
	default:
		a.log.Debug("webhook dispatch failed", "err", err)
		http.Error(w, "dispatch failed", http.StatusInternalServerError)
	}
}
