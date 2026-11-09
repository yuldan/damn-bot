package connectors

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
)

// Framework bounds of SPEC §9.9. Every value is a hard cap: config can lower
// it, never raise it beyond these.
const (
	DefaultTimeout     = 20 * time.Second
	DefaultMaxBytes    = 8 << 20
	maxErrorBody       = 8 << 10
	maxRetryAfter      = 5 * time.Second
	defaultBackoff     = 250 * time.Millisecond
	internalTokenSkew  = 60 * time.Second
	maxPagesDefault    = 5
	maxPaginationItems = 200
)

// Request is one bounded HTTP call. Retry is only ever set for an idempotent
// operation: a POST that creates a comment or a subscription is sent once.
type Request struct {
	Method  string
	URL     string
	Header  http.Header
	Body    []byte
	Retry   bool
	Accept  string
	MaxBody int64
}

// Doer executes connector requests through one registered netx egress client.
type Doer struct {
	client   *netx.Client
	timeout  time.Duration
	retries  int
	maxBytes int64
	redactor *Redactor
	now      func() time.Time
	sleep    func(context.Context, time.Duration) error
}

// DoerOptions wires a Doer. Client is required; every other field has a
// bounded default.
type DoerOptions struct {
	Client   *netx.Client
	Timeout  time.Duration
	Retries  int
	MaxBytes int64
	Redactor *Redactor
	Now      func() time.Time
	Sleep    func(context.Context, time.Duration) error
}

// NewDoer builds a Doer. An invalid option fails closed at wiring time.
func NewDoer(o DoerOptions) (*Doer, error) {
	if o.Client == nil {
		return nil, fmt.Errorf("%w: nil egress client", ErrInvalidConnector)
	}
	d := &Doer{
		client:   o.Client,
		timeout:  o.Timeout,
		retries:  o.Retries,
		maxBytes: o.MaxBytes,
		redactor: o.Redactor,
		now:      o.Now,
		sleep:    o.Sleep,
	}
	switch {
	case d.timeout <= 0:
		d.timeout = DefaultTimeout
	case d.timeout > MaxTimeout:
		return nil, fmt.Errorf("%w: timeout %s exceeds %s", ErrInvalidConnector, d.timeout, MaxTimeout)
	}
	switch {
	case d.retries < 0:
		return nil, fmt.Errorf("%w: negative retries", ErrInvalidConnector)
	case d.retries > MaxRetries:
		return nil, fmt.Errorf("%w: retries %d exceeds %d", ErrInvalidConnector, d.retries, MaxRetries)
	}
	switch {
	case d.maxBytes <= 0:
		d.maxBytes = DefaultMaxBytes
	case d.maxBytes > MaxResponseBytes:
		return nil, fmt.Errorf("%w: max bytes %d exceeds %d", ErrInvalidConnector, d.maxBytes, MaxResponseBytes)
	}
	if d.redactor == nil {
		d.redactor = NewRedactor()
	}
	if d.now == nil {
		d.now = time.Now
	}
	if d.sleep == nil {
		d.sleep = sleepContext
	}
	return d, nil
}

// JSON executes r and decodes a 2xx body into out (when out is non-nil).
// Retries are bounded and only happen for an idempotent Request; a 429 or 5xx
// that outlives the budget becomes a typed network error carrying the last
// Retry-After. Every error text passes through the redactor.
func (d *Doer) JSON(ctx context.Context, r *Request, out any) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, d.contextError(ctx, err)
	}
	attempts := 1
	if r.Retry {
		attempts += d.retries
	}
	var last *Error
	for attempt := range attempts {
		if attempt > 0 {
			if err := d.sleep(ctx, d.backoff(attempt, last)); err != nil {
				return 0, d.contextError(ctx, err)
			}
		}
		status, err := d.once(ctx, r, out)
		if err == nil {
			return status, nil
		}
		ce, ok := errors.AsType[*Error](err)
		if !ok || !retryableStatus(ce.Status) || attempt == attempts-1 {
			return status, err
		}
		last = ce
	}
	return 0, last
}

// once performs exactly one attempt inside the per-call deadline.
func (d *Doer) once(ctx context.Context, r *Request, out any) (status int, err error) {
	// The runtime installs the Authorization value from its own credential
	// source; the model never supplies it, so the DLP gate exempts exactly
	// that header (GAP-SEC-16).
	ctx = netx.WithAuthHeaders(ctx, "Authorization")
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	req, err := d.newRequest(ctx, r)
	if err != nil {
		return 0, err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return 0, d.transportError(ctx, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && err == nil {
			err = &Error{Code: codeNetwork, Message: "close response: " + d.redactor.Redact(cerr.Error()), err: cerr}
		}
	}()
	return d.finish(resp, r.MaxBody, out)
}

// newRequest builds one attempt's request and copies the caller headers.
func (d *Doer) newRequest(ctx context.Context, r *Request) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, bytes.NewReader(r.Body))
	if err != nil {
		return nil, &Error{Code: codeInvalidArgs, Message: d.redactor.Redact(err.Error()), err: err}
	}
	if r.Accept != "" {
		req.Header.Set("Accept", r.Accept)
	}
	for k, vs := range r.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	return req, nil
}

// finish reads and decodes one response inside the byte cap.
func (d *Doer) finish(resp *http.Response, maxBody int64, out any) (int, error) {
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, d.statusError(resp)
	}
	limit := d.maxBytes
	if maxBody > 0 && maxBody < limit {
		limit = maxBody
	}
	data, err := readBounded(resp.Body, limit)
	if err != nil {
		return resp.StatusCode, d.readError(resp.StatusCode, limit, err)
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return resp.StatusCode, nil
	}
	if uerr := json.Unmarshal(data, out); uerr != nil {
		return resp.StatusCode, &Error{
			Code: codeInternal, Message: d.redactor.Redact("decode response: " + uerr.Error()), err: uerr,
		}
	}
	return resp.StatusCode, nil
}

// readError maps a bounded-read failure onto too_large or network.
func (d *Doer) readError(status int, limit int64, err error) *Error {
	if errors.Is(err, errTooLarge) {
		return &Error{Code: codeTooLarge, Message: fmt.Sprintf("response exceeds %d bytes", limit), Status: status}
	}
	return &Error{Code: codeNetwork, Message: d.redactor.Redact(err.Error()), err: err}
}

// statusError maps one non-2xx response onto the closed code set. 413 is
// too_large, 401/403 are auth_required/denied; the body is bounded and
// redacted before it appears in the message.
func (d *Doer) statusError(resp *http.Response) *Error {
	code := codeNetwork
	switch {
	case resp.StatusCode == http.StatusRequestEntityTooLarge:
		code = codeTooLarge
	case resp.StatusCode == http.StatusUnauthorized:
		code = codeAuthRequired
	case resp.StatusCode == http.StatusForbidden:
		code = codeDenied
	case resp.StatusCode == http.StatusNotFound:
		code = codeNotFound
	case resp.StatusCode == http.StatusTooManyRequests:
		code = codeNetwork
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		code = codeInvalidArgs
	}
	e := &Error{Code: code, Status: resp.StatusCode}
	if ra := retryAfter(resp.Header); ra > 0 {
		e.RetryAfter = int64(ra / time.Second)
	}
	if body := redactedBody(resp, maxErrorBody, d.redactor); body != "" {
		e.Message = body
	} else {
		e.Message = http.StatusText(resp.StatusCode)
	}
	return e
}

// transportError classifies a dial/read failure. A deadline is a timeout; an
// explicit cancellation keeps the caller's context error for errors.Is.
func (d *Doer) transportError(ctx context.Context, err error) *Error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &Error{Code: codeTimeout, Message: "request deadline exceeded", err: err}
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return &Error{Code: codeNetwork, Message: "request cancelled", err: context.Canceled}
	}
	return &Error{Code: codeNetwork, Message: d.redactor.Redact(err.Error()), err: err}
}

func (d *Doer) contextError(_ context.Context, err error) *Error {
	if errors.Is(err, context.DeadlineExceeded) {
		return &Error{Code: codeTimeout, Message: "request deadline exceeded", err: err}
	}
	if errors.Is(err, context.Canceled) {
		return &Error{Code: codeNetwork, Message: "request cancelled", err: context.Canceled}
	}
	return &Error{Code: codeNetwork, Message: d.redactor.Redact(err.Error()), err: err}
}

// backoff returns the wait before the next attempt: the server's Retry-After
// when it sent one, otherwise a bounded exponential delay.
func (d *Doer) backoff(attempt int, last *Error) time.Duration {
	if last != nil && last.RetryAfter > 0 {
		return min(time.Duration(last.RetryAfter)*time.Second, maxRetryAfter)
	}
	delay := defaultBackoff << (attempt - 1)
	return min(delay, maxRetryAfter)
}

func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// retryAfter parses the Retry-After header (seconds or HTTP date), capped.
func retryAfter(h http.Header) time.Duration {
	value := strings.TrimSpace(h.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if secs, err := strconv.Atoi(value); err == nil && secs > 0 {
		return min(time.Duration(secs)*time.Second, maxRetryAfter)
	}
	if at, err := http.ParseTime(value); err == nil {
		if d := time.Until(at); d > 0 {
			return min(d, maxRetryAfter)
		}
	}
	return 0
}

// sleepContext waits for d or the context, whichever comes first.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("connectors: wait: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

var errTooLarge = errors.New("connectors: response too large")

// readBounded reads at most limit bytes and reports errTooLarge when the
// stream is longer. It never allocates past limit+1.
func readBounded(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = DefaultMaxBytes
	}
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("connectors: read response: %w", err)
	}
	if n > limit {
		return nil, errTooLarge
	}
	return buf.Bytes(), nil
}

// decodeArgs decodes tool arguments strictly and maps a failure to the
// closed invalid_args code.
func decodeArgs(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return &Error{Code: codeInvalidArgs, Message: "arguments: " + err.Error(), err: err}
	}
	return nil
}

// joinURL joins a base endpoint and an API path without double slashes.
func joinURL(base, path string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

// escapePath escapes one path segment and rejects control characters, so a
// model-supplied id can never rewrite the request target.
func escapePath(segment string) (string, error) {
	if segment == "" || strings.ContainsAny(segment, "\x00\r\n") {
		return "", &Error{Code: codeInvalidArgs, Message: "empty or control character in path id"}
	}
	return url.PathEscape(segment), nil
}

// redactedBody reads at most max bytes of an error body and redacts it.
func redactedBody(resp *http.Response, max int64, red *Redactor) string {
	body, err := readBounded(resp.Body, max)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(red.Redact(string(body)))
}

// headerJSON is the common content type of a connector request body.
func headerJSON() http.Header {
	return http.Header{"Content-Type": {"application/json"}}
}
