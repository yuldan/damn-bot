package gwwa

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/damnbot/damnbot/internal/netx"
)

// NewHTTP returns the loopback HTTP transport: JSON-RPC POST `<endpoint>/rpc`
// with a bearer token and a newline-delimited notification stream at
// `<endpoint>/events` (SPEC §9.7.1). The endpoint must be http loopback.
func NewHTTP(endpoint, token string, hc *http.Client) (Transport, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.Scheme != "http" {
		return nil, fmt.Errorf("%w: http endpoint must be loopback http", ErrProtocol)
	}
	host := parsed.Hostname()
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return nil, fmt.Errorf("%w: http endpoint must be loopback", ErrProtocol)
	}
	if hc == nil {
		hc = netx.LoopbackClient()
	}
	return &httpTransport{
		base:   strings.TrimRight(parsed.String(), "/"),
		token:  token,
		client: hc,
		notif:  make(chan []byte, 32),
	}, nil
}

type httpTransport struct {
	base   string
	token  string
	client *http.Client
	notif  chan []byte
	cancel context.CancelFunc
	once   sync.Once
}

// RoundTrip implements Transport.
func (t *httpTransport) RoundTrip(ctx context.Context, frame []byte) ([]byte, error) {
	req, err := t.request(ctx, http.MethodPost, "/rpc", strings.NewReader(string(frame)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gwwa: rpc: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxFrame+1))
	closeErr := resp.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("gwwa: read rpc: %w", readErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("gwwa: close rpc: %w", closeErr)
	}
	if len(body) > MaxFrame {
		return nil, ErrFrameTooLarge
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: rpc status %d", statusError(resp.StatusCode), resp.StatusCode)
	}
	return body, nil
}

func (t *httpTransport) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, t.base+path, body)
	if err != nil {
		return nil, fmt.Errorf("gwwa: request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+t.token)
	return req, nil
}

// Notifications implements Transport: one long-lived NDJSON GET.
func (t *httpTransport) Notifications(ctx context.Context) (<-chan []byte, error) {
	streamCtx, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	req, err := t.request(streamCtx, http.MethodGet, "/events", nil)
	if err != nil {
		cancel()
		return nil, err
	}
	req.Header.Set("Accept", "application/x-ndjson")
	resp, err := t.client.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("gwwa: events: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		cancel()
		closeErr := resp.Body.Close()
		if closeErr != nil {
			return nil, fmt.Errorf("%w: events status %d: %w", statusError(resp.StatusCode), resp.StatusCode, closeErr)
		}
		return nil, fmt.Errorf("%w: events status %d", statusError(resp.StatusCode), resp.StatusCode)
	}
	go t.readEvents(resp.Body)
	return t.notif, nil
}

func (t *httpTransport) readEvents(body io.ReadCloser) {
	defer close(t.notif)
	defer func() {
		if err := body.Close(); err != nil {
			return
		}
	}()
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), MaxFrame)
	for scanner.Scan() {
		if line := strings.TrimSpace(scanner.Text()); line != "" {
			t.notif <- []byte(line)
		}
	}
}

// Close implements Transport.
func (t *httpTransport) Close() error {
	t.once.Do(func() {
		if t.cancel != nil {
			t.cancel()
		}
	})
	return nil
}

func statusError(code int) error {
	if code == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	return ErrProtocol
}
