package netx

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sync/atomic"
	"time"

	"github.com/damnbot/damnbot/internal/netx/retry"
)

// Purpose labels an egress point (GAP-OPS-02 egress registry).
type Purpose uint8

// Egress purposes. The zero value is invalid: a client that forgets its
// purpose is refused rather than silently skipping the DLP gate.
const (
	// Inference: calls to the configured LLM provider.
	Inference Purpose = iota + 1
	// Tool: requests built from tool arguments (web_fetch, …): public
	// addresses only, no proxy, DLP gate.
	Tool
	// Background: non-inference traffic the user did not ask for (update
	// check, /models refresh, catalogs); off when network.background = false.
	Background
)

// Config is the network policy, copied by [New] (EC-PROV-028: later
// changes to the caller's value have no effect).
type Config struct {
	Offline      bool // network.offline: only loopback dials (CROSS-CUTTING §4)
	NoBackground bool // network.background = false: refuse Background egress
	// Secrets are the known secret values (vault, env, OAuth) the DLP gate
	// blocks in Tool requests and redacts from logged fields; replace them
	// later with [Network.SetSecrets].
	Secrets []string
	// Log receives DEBUG request metadata (never bodies or queries); nil
	// discards it.
	Log *slog.Logger
}

// blockedField replaces host and path in the log line of a DLP-blocked request.
const blockedField = "[blocked]"

// ErrPurpose refuses a client created with an unknown Purpose (incl. 0).
var ErrPurpose = errors.New("netx: invalid egress purpose")

// Transport tuning (ADR-100): pooled keep-alive connections, HTTP/2.
const (
	maxIdleConnsPerHost = 16
	idleConnTimeout     = 90 * time.Second
	tlsHandshakeTimeout = 10 * time.Second
)

// Network owns the process's HTTP transports; create one in the composition
// root and derive every client from it so connections are pooled (§17.2).
type Network struct {
	cfg   Config
	dlp   atomic.Pointer[dlp]
	infra *http.Transport // Inference + Background: env proxy honoured
	tool  *http.Transport // Tool: direct, public addresses only
}

// New builds a Network. Dials from test binaries are loopback-only (E-69).
func New(cfg Config) *Network {
	test := testBinary()
	cfg.Secrets = slices.Clone(cfg.Secrets)
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	n := &Network{
		cfg:   cfg,
		infra: transport(dialRule{offline: cfg.Offline, test: test}, http.ProxyFromEnvironment),
		tool:  transport(dialRule{offline: cfg.Offline, tool: true, test: test}, nil),
	}
	n.dlp.Store(newDLP(cfg.Secrets))
	return n
}

// SetSecrets atomically replaces the known secret values (vault unlock,
// OAuth refresh); requests already past the gate are not rescanned.
func (n *Network) SetSecrets(secrets []string) { n.dlp.Store(newDLP(slices.Clone(secrets))) }

func transport(rule dialRule, proxy func(*http.Request) (*url.URL, error)) *http.Transport {
	return &http.Transport{
		Proxy:               proxy,
		DialContext:         dialer(rule),
		ForceAttemptHTTP2:   true,
		MaxIdleConnsPerHost: maxIdleConnsPerHost,
		IdleConnTimeout:     idleConnTimeout,
		TLSHandshakeTimeout: tlsHandshakeTimeout,
	}
}

// CloseIdleConnections drops pooled idle connections; the Network stays
// usable (new requests dial again).
func (n *Network) CloseIdleConnections() {
	n.infra.CloseIdleConnections()
	n.tool.CloseIdleConnections()
}

// Client is one registered egress point. Its zero value is not usable.
type Client struct {
	n      *Network
	name   string
	refuse error // non-nil: every request fails with it (policy)
	scan   bool  // Tool: DLP gate before sending
	hc     *http.Client
	idle   time.Duration // stream idle limit; StreamIdle (tests shorten it)
}

// Client returns the egress point name with purpose p.
func (n *Network) Client(name string, p Purpose) *Client {
	c := &Client{n: n, name: name, hc: &http.Client{Transport: n.infra, CheckRedirect: checkRedirect(p)}, idle: StreamIdle}
	switch {
	case p == Tool:
		c.hc.Transport, c.scan = n.tool, true
	case p == Background && n.cfg.NoBackground:
		c.refuse = refuse(ErrEgressDisabled)
	case p != Inference && p != Background:
		c.refuse = &classedError{err: fmt.Errorf("%w %d", ErrPurpose, p), class: retry.BadRequest}
	}
	return c
}

// maxRedirects is net/http's default chain limit.
const maxRedirects = 10

// checkRedirect is the redirect policy (REVIEW-F1-SEC3 PROV-M4, ADR-100):
// Inference never follows one (the prompt and Route.Headers credentials
// would reach the target; 3xx is returned as a status); other purposes
// follow GET/HEAD only, never from https to http, and drop every header
// but Accept/Accept-Language/User-Agent when the host changes (net/http
// only drops Authorization and Cookie). A refused redirect returns the
// 3xx response itself.
func checkRedirect(p Purpose) func(*http.Request, []*http.Request) error {
	if p == Inference {
		return func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return func(req *http.Request, via []*http.Request) error {
		first := via[0]
		switch {
		case len(via) >= maxRedirects,
			first.Method != http.MethodGet && first.Method != http.MethodHead,
			first.URL.Scheme == "https" && req.URL.Scheme != "https":
			return http.ErrUseLastResponse
		}
		if req.URL.Host != first.URL.Host {
			for k := range req.Header {
				if k != "Accept" && k != "Accept-Language" && k != "User-Agent" {
					req.Header.Del(k)
				}
			}
		}
		return nil
	}
}

// Do sends req. Any status is returned as a response, like net/http;
// transport errors are *ConnectError (nothing sent) or wrap
// ErrConnectionLost, and never carry the URL (it may hold credentials).
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if c.refuse != nil {
		return nil, fmt.Errorf("%s %s: %w", c.name, req.URL.Host, c.refuse)
	}
	if c.scan {
		if err := c.n.dlp.Load().check(req); err != nil {
			// The URL may carry the secret (also in the host): never echo it.
			c.logRequest(req, nil, err, 0)
			return nil, fmt.Errorf("%s %s: %w", c.name, req.Method, err)
		}
	}
	start := time.Now()
	resp, err := c.hc.Do(req)
	c.logRequest(req, resp, err, time.Since(start))
	if err != nil {
		if ue, ok := errors.AsType[*url.Error](err); ok {
			err = ue.Err // drop the URL: it may hold credentials
		}
		return nil, fmt.Errorf("%s %s %s: %w", c.name, req.Method, req.URL.Host, classify(err))
	}
	return resp, nil
}

// DoOK is [Client.Do] that turns a non-2xx status into a *StatusError
// (body read up to 64 KiB and closed).
func (c *Client) DoOK(req *http.Request) (*http.Response, error) {
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	cerr := resp.Body.Close()
	se := &StatusError{StatusCode: resp.StatusCode, Header: resp.Header, Body: body}
	if err := errors.Join(rerr, cerr); err != nil {
		return nil, fmt.Errorf("%s: %w (body: %w)", c.name, se, classify(err))
	}
	return nil, fmt.Errorf("%s: %w", c.name, se)
}

// logRequest writes one DEBUG line of request metadata (SPEC §15, ADR-071
// butir 5): egress point, method, host, redacted path, sizes, status or
// failure class, duration. Never the query, headers, or bodies.
func (c *Client) logRequest(req *http.Request, resp *http.Response, err error, d time.Duration) {
	ctx := req.Context()
	if !c.n.cfg.Log.Enabled(ctx, slog.LevelDebug) {
		return
	}
	host, path := blockedField, blockedField
	if !errors.Is(err, ErrSecretEgress) && !errors.Is(err, ErrDLPLimit) {
		r := c.n.dlp.Load().log
		host, path = r.String(req.URL.Host), r.String(req.URL.EscapedPath())
	}
	attrs := []slog.Attr{
		slog.String("egress", c.name),
		slog.String("method", req.Method),
		slog.String("host", host),
		slog.String("path", path),
		slog.Int64("req_bytes", req.ContentLength),
		slog.Duration("duration", d),
	}
	if resp != nil {
		attrs = append(attrs, slog.Int("status", resp.StatusCode), slog.Int64("resp_bytes", resp.ContentLength))
	}
	if err != nil {
		attrs = append(attrs, slog.String("class", retry.ClassifyErr(err).String()))
	}
	c.n.cfg.Log.LogAttrs(ctx, slog.LevelDebug, "http request", attrs...)
}
