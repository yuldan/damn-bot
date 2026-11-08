package gwplat

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
)

// Inbound HTTP errors. Compared with errors.Is.
var (
	ErrBodyTooLarge = errors.New("gwplat: request body too large")
	ErrBadPayload   = errors.New("gwplat: invalid payload")
)

// HTTP is the shared inbound/outbound transport seam of the REST adapters.
// Client is injected so adapters never invent egress; production passes netx.
type HTTP struct {
	Client  *http.Client
	MaxBody int64
}

// StatusError is a non-2xx platform answer. It never carries the body, which
// may echo credentials.
type StatusError struct {
	Code int
}

// Error implements error.
func (e *StatusError) Error() string { return fmt.Sprintf("gwplat: status %d", e.Code) }

// ReadBody bounds one callback body, answering 413/405 on failure.
func (h HTTP) ReadBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return nil, ErrBadPayload
	}
	limit := h.MaxBody
	if limit <= 0 {
		limit = 1 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return nil, ErrBodyTooLarge
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return nil, ErrBadPayload
	}
	return body, nil
}

// Do sends one request and returns a bounded body.
func (h HTTP) Do(ctx context.Context, method, rawURL string, body []byte, headers map[string]string) ([]byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
	if err != nil {
		return nil, fmt.Errorf("gwplat: request: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gwplat: transport: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			return
		}
	}()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("gwplat: response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &StatusError{Code: resp.StatusCode}
	}
	return data, nil
}

// DoJSON posts one JSON body.
func (h HTTP) DoJSON(ctx context.Context, rawURL string, body any, headers map[string]string) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("gwplat: encode: %w", err)
	}
	out := make(map[string]string, len(headers)+1)
	maps.Copy(out, headers)
	out["Content-Type"] = "application/json"
	return h.Do(ctx, http.MethodPost, rawURL, payload, out)
}

// Deny writes the one non-leaking rejection response.
func Deny(w http.ResponseWriter, status int) {
	if status == 0 {
		status = http.StatusUnauthorized
	}
	http.Error(w, "forbidden", status)
}

// ConstantTimeEqual compares two strings without an early exit.
func ConstantTimeEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
