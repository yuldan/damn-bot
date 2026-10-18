package netx

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Purpose labels an egress point (GAP-OPS-02 egress registry).
type Purpose uint8

// Egress purposes.
const (
	// Inference: calls to the configured LLM provider.
	Inference Purpose = iota
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
}

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
	infra *http.Transport // Inference + Background: env proxy honoured
	tool  *http.Transport // Tool: direct, public addresses only
}

// New builds a Network. Dials from test binaries are loopback-only (E-69).
func New(cfg Config) *Network {
	test := testBinary()
	return &Network{
		cfg:   cfg,
		infra: transport(dialRule{offline: cfg.Offline, test: test}, http.ProxyFromEnvironment),
		tool:  transport(dialRule{offline: cfg.Offline, tool: true, test: test}, nil),
	}
}

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
	name   string
	refuse error // non-nil: every request fails with it (policy)
	hc     *http.Client
	idle   time.Duration // stream idle limit; StreamIdle (tests shorten it)
}

// Client returns the egress point name with purpose p.
func (n *Network) Client(name string, p Purpose) *Client {
	c := &Client{name: name, hc: &http.Client{Transport: n.infra}, idle: StreamIdle}
	switch {
	case p == Tool:
		c.hc.Transport = n.tool
	case p == Background && n.cfg.NoBackground:
		c.refuse = refuse(ErrEgressDisabled)
	}
	return c
}

// Do sends req. Any status is returned as a response, like net/http;
// transport errors are *ConnectError (nothing sent) or wrap
// ErrConnectionLost, and never carry the URL (it may hold credentials).
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if c.refuse != nil {
		return nil, fmt.Errorf("%s %s: %w", c.name, req.URL.Host, c.refuse)
	}
	resp, err := c.hc.Do(req)
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
