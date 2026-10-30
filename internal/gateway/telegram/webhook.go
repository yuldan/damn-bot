package telegram

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// WebhookSecretHeader carries the secret_token configured by setWebhook.
const WebhookSecretHeader = "X-Telegram-Bot-Api-Secret-Token" // #nosec G101 -- header name, not a credential

const (
	webhookBodyLimit  = 1 << 20 // Telegram updates are far smaller; 1 MiB bounds a forged body.
	webhookTimeout    = 10 * time.Second
	webhookSeenLimit  = 1024
	webhookSecretMax  = 256
	webhookSecretNote = "1-256 characters of A-Z, a-z, 0-9, '_' or '-'"
)

// ErrWebhookSecret rejects a secret the Bot API would refuse.
var ErrWebhookSecret = fmt.Errorf("telegram: webhook secret must be %s", webhookSecretNote)

// ErrWebhookURL reports a missing setWebhook target.
var ErrWebhookURL = errors.New("telegram: webhook url is required")

var (
	errWebhookPayload = errors.New("telegram: invalid webhook payload")
	errWebhookTooBig  = errors.New("telegram: webhook payload too large")
)

// WebhookOptions configures the inbound handler. In is the same channel Run
// delivers to, so webhook and polling share one admission pipeline.
type WebhookOptions struct {
	Secret  string
	In      chan<- Inbound
	MaxBody int64
	Timeout time.Duration
	Seen    int
}

// ValidWebhookSecret reports whether s is accepted by setWebhook.
func ValidWebhookSecret(s string) bool {
	if len(s) == 0 || len(s) > webhookSecretMax {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// SetWebhook registers url and its secret_token. It never touches
// drop_pending_updates: pending updates stay on Telegram's side.
func (a *Adapter) SetWebhook(ctx context.Context, hookURL, secret string) error {
	if !ValidWebhookSecret(secret) {
		return ErrWebhookSecret
	}
	if strings.TrimSpace(hookURL) == "" {
		return ErrWebhookURL
	}
	form := url.Values{"url": {hookURL}, "secret_token": {secret}}
	if err := a.call(ctx, "setWebhook", form, nil); err != nil {
		return fmt.Errorf("telegram: setWebhook: %w", err)
	}
	return nil
}

// DeleteWebhook removes the registration before a planned stop so Telegram
// queues updates instead of retrying a closed listener; it never drops them.
func (a *Adapter) DeleteWebhook(ctx context.Context) error {
	if err := a.call(ctx, "deleteWebhook", url.Values{}, nil); err != nil {
		return fmt.Errorf("telegram: deleteWebhook: %w", err)
	}
	return nil
}

// WebhookHandler authenticates one update per request (403, 413, 200 after commit, 503).
func (a *Adapter) WebhookHandler(opts WebhookOptions) (http.Handler, error) {
	if !ValidWebhookSecret(opts.Secret) {
		return nil, ErrWebhookSecret
	}
	if opts.In == nil {
		return nil, errors.New("telegram: webhook inbound channel is required")
	}
	maxBody := opts.MaxBody
	if maxBody <= 0 {
		maxBody = webhookBodyLimit
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = webhookTimeout
	}
	seen := opts.Seen
	if seen <= 0 {
		seen = webhookSeenLimit
	}
	return &webhookHandler{
		adapter: a, in: opts.In, want: sha256.Sum256([]byte(opts.Secret)),
		maxBody: maxBody, timeout: timeout, seen: newSeenUpdates(seen),
	}, nil
}

type webhookHandler struct {
	adapter *Adapter
	in      chan<- Inbound
	want    [sha256.Size]byte
	maxBody int64
	timeout time.Duration
	seen    *seenUpdates
}

func (h *webhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !h.authorized(r.Header.Get(WebhookSecretHeader)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	u, status, err := h.decode(w, r)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	if !h.seen.claim(u.UpdateID) {
		w.WriteHeader(http.StatusOK) // duplicate delivery: already admitted
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()
	if u.CallbackQuery != nil {
		if derr := h.adapter.handleCallback(ctx, u.CallbackQuery); derr != nil {
			h.seen.release(u.UpdateID)
			http.Error(w, "inbound unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	if u.Message == nil {
		w.WriteHeader(http.StatusOK)
		return
	}
	ib, ok := h.adapter.inboundOf(&u)
	if !ok {
		w.WriteHeader(http.StatusOK)
		return
	}
	// 200 is sent only after Gate.Admit reports the gateway_inbound row
	// durable (or already present), never on the in-memory handoff alone
	// (SPEC §9.7). A failed commit stays non-2xx so Telegram retries.
	committed := make(chan error, 1)
	var once sync.Once
	ib.Commit = func(err error) { once.Do(func() { committed <- err }) }
	if derr := h.adapter.deliverInbound(ctx, &ib, h.in); derr != nil {
		h.seen.release(u.UpdateID)
		http.Error(w, "inbound unavailable", http.StatusServiceUnavailable)
		return
	}
	select {
	case cerr := <-committed:
		if cerr != nil {
			h.seen.release(u.UpdateID)
			http.Error(w, "inbound commit failed", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	case <-ctx.Done():
		h.seen.release(u.UpdateID)
		http.Error(w, "inbound unavailable", http.StatusServiceUnavailable)
	}
}

func (h *webhookHandler) authorized(got string) bool {
	sum := sha256.Sum256([]byte(got))
	return subtle.ConstantTimeCompare(sum[:], h.want[:]) == 1
}

func (h *webhookHandler) decode(w http.ResponseWriter, r *http.Request) (update, int, error) {
	defer r.Body.Close() //nolint:errcheck // the server owns the request body
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBody)
	dec := json.NewDecoder(r.Body)
	var u update
	if err := dec.Decode(&u); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			return u, http.StatusRequestEntityTooLarge, errWebhookTooBig
		}
		return u, http.StatusBadRequest, errWebhookPayload
	}
	var extra json.RawMessage
	switch err := dec.Decode(&extra); {
	case errors.Is(err, io.EOF):
	case err != nil:
		return u, http.StatusBadRequest, errWebhookPayload
	default:
		return u, http.StatusBadRequest, errWebhookPayload
	}
	if u.UpdateID <= 0 {
		return u, http.StatusBadRequest, errWebhookPayload
	}
	return u, http.StatusOK, nil
}

// seenUpdates spares a second handoff for a retried update; the DB row is the authority.
type seenUpdates struct {
	mu    sync.Mutex
	limit int
	ids   map[int64]struct{}
}

func newSeenUpdates(limit int) *seenUpdates {
	return &seenUpdates{limit: limit, ids: make(map[int64]struct{}, limit)}
}

func (s *seenUpdates) claim(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ids[id]; ok {
		return false
	}
	if len(s.ids) >= s.limit {
		for old := range s.ids {
			delete(s.ids, old)
			break
		}
	}
	s.ids[id] = struct{}{}
	return true
}

func (s *seenUpdates) release(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ids, id)
}
