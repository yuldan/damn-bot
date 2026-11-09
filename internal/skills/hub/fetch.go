package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"
)

// Fetch limits. A SKILL.md is capped by the skills package itself; the index
// is larger because it carries every entry. The fetcher reads one byte past a
// limit so an oversized body is rejected instead of silently truncated.
const (
	MaxIndexBytes = 1 << 20
	MaxSkillBytes = 8192
	maxRedirects  = 3
	userAgent     = "damnbot/skills-hub"
)

// Errors of the fetch and verification path.
var (
	ErrTooLarge   = errors.New("skills hub: body too large")
	ErrBinary     = errors.New("skills hub: body is binary")
	ErrRedirect   = errors.New("skills hub: cross-host or too many redirects")
	ErrStatus     = errors.New("skills hub: unexpected status")
	ErrHash       = errors.New("skills hub: sha256 mismatch")
	ErrNoEntry    = errors.New("skills hub: entry missing from index")
	ErrIndex      = errors.New("skills hub: invalid index")
	ErrTestSource = errors.New("skills hub: test endpoint outside a test build")
)

// Fetcher is the one egress seam of the hub. The composition root supplies an
// HTTP client built on netx, so the hub never creates a transport of its own
// (E-05) and unit tests can replace the network with a map.
type Fetcher interface {
	Get(ctx context.Context, u *url.URL, limit int64) ([]byte, error)
}

// HTTPFetcher is the production [Fetcher]. It clones the supplied client and
// installs a same-host redirect policy, so a manifest or archive can never
// arrive from a host other than the one the operator allowlisted.
type HTTPFetcher struct {
	client *http.Client
}

// NewHTTPFetcher validates the client and returns a fetcher with the hub's
// redirect policy. The client's transport is used unchanged.
func NewHTTPFetcher(client *http.Client) (*HTTPFetcher, error) {
	if client == nil {
		return nil, fmt.Errorf("%w: nil http client", ErrURL)
	}
	clone := *client
	clone.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects || req.URL.Host != via[0].URL.Host {
			return http.ErrUseLastResponse
		}
		return nil
	}
	return &HTTPFetcher{client: &clone}, nil
}

// Get fetches one URL, bounded to limit bytes, and accepts only a 200 text
// body. A cross-host redirect is returned as a 3xx and rejected, not followed.
func (f *HTTPFetcher) Get(ctx context.Context, u *url.URL, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: request: %w", ErrURL, err)
	}
	req.Header.Set("Accept", "application/json, text/markdown;q=0.9, text/plain;q=0.8")
	req.Header.Set("User-Agent", userAgent)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("skills hub: get %s: %w", u.Redacted(), err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // body fully read; close error is not actionable
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, fmt.Errorf("%w: %s", ErrRedirect, u.Redacted())
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !textual(ct) {
		return nil, fmt.Errorf("%w: content-type %q", ErrBinary, ct)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: %d", ErrStatus, resp.StatusCode)
	}
	return readBounded(resp.Body, limit)
}

// textual accepts the JSON/markdown/text families a registry may use.
func textual(ct string) bool {
	base, _, _ := strings.Cut(ct, ";")
	base = strings.ToLower(strings.TrimSpace(base))
	return base == "application/json" || base == "text/plain" || base == "text/markdown" ||
		base == "application/octet-stream" || strings.HasSuffix(base, "+json")
}

// readBounded reads at most limit bytes and rejects the body when it is
// longer, is not UTF-8, or carries a NUL. Bounded content is the only content
// the hub will hash, parse, or store.
func readBounded(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("%w: non-positive limit", ErrTooLarge)
	}
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("skills hub: read body: %w", err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%w: > %d bytes", ErrTooLarge, limit)
	}
	if !utf8.Valid(body) || slices.Contains(body, 0) {
		return nil, fmt.Errorf("%w: not UTF-8 text", ErrBinary)
	}
	return body, nil
}
