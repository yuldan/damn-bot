package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/damnbot/damnbot/internal/gwplat"
)

// Delivery limits of the outbound test sender.
const (
	// DefaultAttempts bounds the retries of one test delivery.
	DefaultAttempts = 3
)

// ErrDelivery is a non-retryable or exhausted delivery failure.
var ErrDelivery = errors.New("webhook: delivery failed")

// Receipt is the outcome of one test delivery.
type Receipt struct {
	ID       string
	Attempts int
	Status   int
}

// Sender posts one signed event to a stored target. Retries are bounded and
// paced by the shared backoff (never time.Sleep).
type Sender struct {
	Client      HTTPDoer
	MaxAttempts int
	Backoff     gwplat.Backoff
	Now         func() time.Time
}

// Send delivers exactly one event: `test` for a generic target and a Graph
// notification for an msgraph target. The secret value signs the body and is
// never returned or logged.
func (s *Sender) Send(ctx context.Context, sub *Subscription, secret []byte, event string) (Receipt, error) {
	if err := ValidateSubscription(sub); err != nil {
		return Receipt{}, err
	}
	if s.Client == nil {
		return Receipt{}, fmt.Errorf("%w: no HTTP client", ErrDelivery)
	}
	body, id, err := s.envelope(sub, secret, event)
	if err != nil {
		return Receipt{}, err
	}
	attempts := s.MaxAttempts
	if attempts <= 0 {
		attempts = DefaultAttempts
	}
	var last error
	for attempt := 1; attempt <= attempts; attempt++ {
		status, err := s.post(ctx, sub.URL, Sign(secret, body), body)
		if err == nil && status < http.StatusInternalServerError {
			return Receipt{ID: id, Attempts: attempt, Status: status}, nil
		}
		last = err
		if attempt == attempts {
			break
		}
		if werr := s.Backoff.Wait(ctx, attempt); werr != nil {
			return Receipt{}, fmt.Errorf("%w: retry wait", ErrDelivery)
		}
	}
	return Receipt{}, last
}

func (s *Sender) envelope(sub *Subscription, secret []byte, event string) ([]byte, string, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	id := fmt.Sprintf("test-%d", now().UTC().UnixNano())
	if sub.Provider == "msgraph" {
		body, err := json.Marshal(map[string]any{
			"value": []map[string]any{{
				"subscriptionId": sub.ID,
				"clientState":    string(secret),
				"changeType":     "updated",
				"resource":       "/chats('webhook-test')/messages('1')",
			}},
		})
		if err != nil {
			return nil, "", fmt.Errorf("webhook: encode graph test event: %w", err)
		}
		return body, id, nil
	}
	env := Envelope{ID: id, Event: event, Chat: "webhook-test", Sender: "damnbot", Text: "webhook test event"}
	body, err := json.Marshal(&env)
	if err != nil {
		return nil, "", fmt.Errorf("webhook: encode test event: %w", err)
	}
	return body, id, nil
}

func (s *Sender) post(ctx context.Context, url, signature string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("%w: request: %w", ErrDelivery, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(SignatureHeader, signature)
	resp, err := s.Client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrDelivery, err)
	}
	defer closeBody(resp)
	if _, cerr := io.Copy(io.Discard, io.LimitReader(resp.Body, MaxResponseBytes)); cerr != nil {
		return resp.StatusCode, fmt.Errorf("%w: response: %w", ErrDelivery, cerr)
	}
	if resp.StatusCode >= http.StatusInternalServerError {
		return resp.StatusCode, fmt.Errorf("%w: status %d", ErrDelivery, resp.StatusCode)
	}
	return resp.StatusCode, nil
}
