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

// WebhookSecretHeader is the header Telegram sends on every webhook request;
// its value is the secret_token supplied to setWebhook (Bot API).
const WebhookSecretHeader = "X-Telegram-Bot-Api-Secret-Token" // #nosec G101 -- header name, not a credential

const (
	webhookBodyLimit  = 1 << 20 // Telegram updates are far smaller; 1 MiB bounds a forged body.
	webhookTimeout    = 10 * time.Second
	webhookSeenLimit  = 1024
	webhookSecretMax  = 256
	webhookSecretNote = "1-256 characters of A-Z, a-z, 0-9, '_' or '-'"
)

// ErrWebhookSecret rejects a secret the Bot API would refuse and a handler
// without one: webhook mode never runs unauthenticated.
var ErrWebhookSecret = fmt.Errorf("telegram: webhook secret must be %s", webhookSecretNote)

// ErrWebhookURL reports a missing setWebhook target.
var ErrWebhookURL = errors.New("telegram: webhook url is required")

var (
	errWebhookPayload = errors.New("telegram: invalid webhook payload")
	errWebhookTooBig  = errors.New("telegram: webhook payload too large")
)

// WebhookOptions configures the inbound HTTP handler. In is the same channel
// Run delivers to, so a webhook update takes the poller's path through the
// gateway (admission, dedupe, turns) without a second pipeline.
type WebhookOptions struct {
	Secret  string
	In      chan<- Inbound
	MaxBody int64
	Timeout time.Duration
	Seen    int
}

// ValidWebhookSecret reports whether s is accepted by setWebhook's
// secret_token field.
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

// SetWebhook registers url and its secret_token with the Bot API. It never
// touches drop_pending_updates: pending updates stay on Telegram's side and a
// later getUpdates (mode switch) or webhook delivery still receives them.
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

// DeleteWebhook removes the webhook registration before a planned stop so
// Telegram queues updates instead of retrying a closed listener. Like
// SetWebhook it never drops pending updates.
func (a *Adapter) DeleteWebhook(ctx context.Context) error {
	if err := a.call(ctx, "deleteWebhook", url.Values{}, nil); err != nil {
		return fmt.Errorf("telegram: deleteWebhook: %w", err)
	}
	return nil
}

// WebhookHandler authenticates and admits one update per HTTP request. It
// answers 200 only after the update is handed to the inbound path, 403
// without the configured secret, 413 above MaxBody, and 503 when the inbound
// channel is not accepting (Telegram then retries).
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
	if err := h.adapter.deliver(ctx, &u, h.in); err != nil {
		h.seen.release(u.UpdateID) // let Telegram's retry through
		http.Error(w, "inbound unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// authorized compares fixed-size digests, so the comparison neither leaks the
// secret length nor short-circuits on the first differing byte.
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

// seenUpdates bounds duplicate detection to the last limit update ids. A
// duplicate is acknowledged but not delivered again, so a Telegram retry of
// an already-admitted update never runs a second turn.
type seenUpdates struct {
	mu    sync.Mutex
	limit int
	ring  []int64
	ids   map[int64]int
	next  int
}

func newSeenUpdates(limit int) *seenUpdates {
	return &seenUpdates{limit: limit, ids: make(map[int64]int, limit), ring: make([]int64, 0, limit)}
}

func (s *seenUpdates) claim(id int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ids[id]; ok {
		return false
	}
	if len(s.ring) < s.limit {
		s.ids[id] = len(s.ring)
		s.ring = append(s.ring, id)
		return true
	}
	old := s.ring[s.next]
	delete(s.ids, old)
	s.ring[s.next] = id
	s.ids[id] = s.next
	s.next = (s.next + 1) % s.limit
	return true
}

// release forgets id after a failed handoff so the retried request is served.
func (s *seenUpdates) release(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ids[id]; !ok {
		return
	}
	delete(s.ids, id)
	kept := s.ring[:0]
	for _, v := range s.ring {
		if v != id {
			kept = append(kept, v)
		}
	}
	s.ring = kept
	for i, v := range s.ring {
		s.ids[v] = i
	}
	s.next = len(s.ring) % s.limit
}
