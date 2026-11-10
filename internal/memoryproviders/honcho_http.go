package memoryproviders

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	honchoErrorBody = 8 << 10
	honchoMaxBody   = 4 << 20
	honchoBackoff   = 100 * time.Millisecond
)

type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.status, e.body)
}

// doJSON runs one bounded request with optional retry. Every attempt shares
// the caller's deadline; a non-retryable status or the last attempt returns
// the typed error unchanged.
func (h *honcho) doJSON(ctx context.Context, method, path string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		body, err = marshalJSON(in)
		if err != nil {
			return err
		}
	}
	attempts := h.retries + 1
	for attempt := range attempts {
		if attempt > 0 {
			if err := h.sleep(ctx, honchoBackoff*time.Duration(attempt)); err != nil {
				return err
			}
		}
		err := h.once(ctx, method, path, body, out)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil || !retryable(err) || attempt == attempts-1 {
			return err
		}
	}
	return fmt.Errorf("%w: retry budget exhausted", ErrResponse)
}

func (h *honcho) once(ctx context.Context, method, path string, body []byte, out any) (err error) {
	req, err := http.NewRequestWithContext(ctx, method, h.base+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.doer.Do(req)
	if err != nil {
		return fmt.Errorf("honcho: request: %w", err)
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg, readErr := io.ReadAll(io.LimitReader(resp.Body, honchoErrorBody))
		if readErr != nil {
			msg = nil
		}
		return &httpError{status: resp.StatusCode, body: string(msg)}
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, honchoMaxBody)).Decode(out); err != nil {
		return fmt.Errorf("%w: decode response: %w", ErrResponse, err)
	}
	return nil
}

func retryable(err error) bool {
	var he *httpError
	if !errors.As(err, &he) {
		return false
	}
	switch he.status {
	case http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}
