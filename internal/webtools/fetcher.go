//go:build !minimal

package webtools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
)

var (
	errBadConfig, errBadURL    = errors.New("webtools: invalid configuration"), errors.New("webtools: invalid URL")
	errScheme, errBlockedHost  = errors.New("webtools: URL scheme must be http or https"), errors.New("webtools: host is not public")
	errOffline, errRedirect    = errors.New("webtools: network.offline is set"), errors.New("webtools: redirect refused")
	errHTTPStatus, errTooLarge = errors.New("webtools: unexpected HTTP status"), errors.New("webtools: response body too large")
	errBinary, errTimeout      = errors.New("webtools: content is not HTML text"), errors.New("webtools: fetch timed out")
)

type (
	hostPolicy  func(host string) error
	fetchConfig struct {
		Offline   bool
		UserAgent string
		MaxBody   int64
		Timeout   time.Duration
		policy    hostPolicy
	}
)

type page struct {
	URL, Host, ContentType string
	Status                 int
	Body                   []byte
}
type fetcher struct {
	client *netx.Client
	cfg    fetchConfig
}

func newFetcher(client *netx.Client, cfg fetchConfig) (*fetcher, error) {
	switch {
	case client == nil:
		return nil, fmt.Errorf("%w: nil netx client", errBadConfig)
	case !strings.HasPrefix(cfg.UserAgent, "damnbot/") || strings.ContainsAny(cfg.UserAgent, " \t\r\n"):
		return nil, fmt.Errorf("%w: User-Agent must be damnbot/<version>", errBadConfig)
	case cfg.MaxBody < 0 || cfg.MaxBody > 16<<20 || cfg.Timeout < 0:
		return nil, fmt.Errorf("%w: limits out of range", errBadConfig)
	}
	if cfg.MaxBody == 0 {
		cfg.MaxBody = 2 << 20
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 20 * time.Second
	}
	if cfg.policy == nil {
		cfg.policy = publicHost
	}
	return &fetcher{client: client, cfg: cfg}, nil
}

func publicHost(host string) error {
	lower := strings.ToLower(host)
	if lower == "" || lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return fmt.Errorf("%w: %q", errBlockedHost, host)
	}
	if ip, err := netip.ParseAddr(host); err == nil &&
		(!ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
		return fmt.Errorf("%w: %s", errBlockedHost, host)
	}
	return nil
}

func (f *fetcher) Fetch(ctx context.Context, rawURL string) (page, error) {
	if f.cfg.Offline {
		return page{}, errOffline
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.User != nil {
		return page{}, fmt.Errorf("%w: %s", errBadURL, rawURL)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return page{}, errScheme
	}
	if perr := f.cfg.policy(u.Hostname()); perr != nil {
		return page{}, perr
	}
	ctx, cancel := context.WithTimeout(ctx, f.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return page{}, fmt.Errorf("%w: %w", errBadURL, err)
	}
	req.Header.Set("User-Agent", f.cfg.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.1")
	resp, err := f.client.WithSameHostRedirects(3).DoOK(req)
	if err != nil {
		return page{}, mapFetchError(ctx, err)
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, f.cfg.MaxBody+1))
	closeErr := resp.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return page{}, fmt.Errorf("webtools: read body: %w", err)
	}
	if int64(len(body)) > f.cfg.MaxBody {
		return page{}, fmt.Errorf("%w: > %d bytes", errTooLarge, f.cfg.MaxBody)
	}
	return page{
		URL: u.String(), Host: u.Hostname(), Status: resp.StatusCode,
		ContentType: resp.Header.Get("Content-Type"), Body: body,
	}, nil
}

func mapFetchError(ctx context.Context, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", errTimeout, err)
	}
	status, ok := errors.AsType[*netx.StatusError](err)
	switch {
	case ok && status.StatusCode >= 300 && status.StatusCode < 400:
		return fmt.Errorf("%w: %d", errRedirect, status.StatusCode)
	case ok:
		return fmt.Errorf("%w: %d", errHTTPStatus, status.StatusCode)
	}
	return fmt.Errorf("webtools: fetch: %w", err)
}
