package a2a

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/netx/retry"
)

// Client defaults.
const (
	defaultTimeout    = 10 * time.Second
	defaultHistory    = 64
	defaultMaxDepth   = 2
	maxOrchestrateFan = 4
)

// Peer is one configured remote agent.
type Peer struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// History is one recorded client interaction.
type History struct {
	Peer   string
	TaskID string
	State  State
	At     time.Time
}

// ClientConfig configures the outbound A2A client. The zero HTTP field takes
// netx.LoopbackClient; the composition root injects the tool-egress client.
type ClientConfig struct {
	Peers         []Peer
	Token         string
	SecretName    string
	Secrets       gwplat.SecretSource
	HTTP          *http.Client
	AllowLoopback bool
	Timeout       time.Duration
	Retry         retry.Policy
	MaxHistory    int
	MaxDepth      int
	Now           func() time.Time
}

// Client is the outbound A2A surface and the backing store of TOOL-001..005.
type Client struct {
	peers   map[string]Peer
	token   string
	http    *http.Client
	timeout time.Duration
	policy  retry.Policy
	history int
	depth   int
	allow   bool
	now     func() time.Time

	mu   sync.Mutex
	hist []History
}

// NewClient validates every peer before any dial and returns a dormant client.
func NewClient(ctx context.Context, cfg *ClientConfig) (*Client, error) {
	if cfg == nil || len(cfg.Peers) == 0 {
		return nil, fmt.Errorf("%w: no peers", ErrConfig)
	}
	peers, err := validatePeers(cfg.Peers, cfg.AllowLoopback)
	if err != nil {
		return nil, err
	}
	token, err := resolveToken(ctx, cfg)
	if err != nil {
		return nil, err
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	history := cfg.MaxHistory
	if history <= 0 {
		history = defaultHistory
	}
	depth := cfg.MaxDepth
	if depth <= 0 {
		depth = defaultMaxDepth
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	client := cfg.HTTP
	if client == nil {
		client = netx.LoopbackClient()
	}
	return &Client{
		peers: peers, token: token, http: client, timeout: timeout, policy: cfg.Retry,
		history: history, depth: depth, allow: cfg.AllowLoopback, now: now,
	}, nil
}

func validatePeers(list []Peer, allowLoopback bool) (map[string]Peer, error) {
	peers := make(map[string]Peer, len(list))
	for _, p := range list {
		if p.Name == "" || strings.ContainsAny(p.Name, " \x00\r\n") {
			return nil, fmt.Errorf("%w: peer name %q", ErrConfig, p.Name)
		}
		if _, dup := peers[p.Name]; dup {
			return nil, fmt.Errorf("%w: duplicate peer %q", ErrConfig, p.Name)
		}
		if err := ValidatePeerURL(p.URL, allowLoopback); err != nil {
			return nil, err
		}
		peers[p.Name] = p
	}
	return peers, nil
}

func resolveToken(ctx context.Context, cfg *ClientConfig) (string, error) {
	if cfg.Token != "" {
		return cfg.Token, nil
	}
	name := cfg.SecretName
	if name == "" {
		name = "a2a_token"
	}
	if cfg.Secrets == nil {
		return "", fmt.Errorf("%w: bearer secret is required", ErrConfig)
	}
	values, err := gwplat.ResolveSecrets(ctx, &gwplat.Descriptor{Name: "a2a", Secrets: []string{name}}, cfg.Secrets)
	if err != nil {
		return "", fmt.Errorf("a2a: bearer secret: %w", err)
	}
	return values[name], nil
}

// ValidatePeerURL is the pre-dial SSRF gate: only http(s), no userinfo, and in
// production only a public host. A private or loopback literal is rejected
// before any request is built.
func ValidatePeerURL(raw string, allowLoopback bool) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: unparseable", ErrPeerURL)
	}
	if u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
		return fmt.Errorf("%w: %s", ErrPeerURL, u.Redacted())
	}
	if allowLoopback {
		return nil
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("%w: %q", ErrPeerURL, host)
	}
	if addr, perr := netip.ParseAddr(host); perr == nil && !publicAddr(addr) {
		return fmt.Errorf("%w: %q", ErrPeerURL, host)
	}
	return nil
}

func publicAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsGlobalUnicast() && !addr.IsPrivate() && !addr.IsLoopback() &&
		!addr.IsLinkLocalUnicast() && !addr.IsLinkLocalMulticast() && !addr.IsMulticast() && !addr.IsUnspecified()
}

// Peers returns the configured peers sorted by name (never a token).
func (c *Client) Peers() []Peer {
	out := make([]Peer, 0, len(c.peers))
	for _, p := range c.peers {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Peer) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// History returns the bounded interaction history, oldest first.
func (c *Client) History() []History {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.hist)
}

// MaxDepth reports the configured orchestration depth.
func (c *Client) MaxDepth() int { return c.depth }

// MaxFanOut reports the per-orchestration fan-out cap.
func (c *Client) MaxFanOut() int { return maxOrchestrateFan }

// Send submits a task. An empty TaskID is generated here, so a retried send
// is idempotent on the peer.
func (c *Client) Send(ctx context.Context, peer string, req Request) (Task, error) {
	if req.TaskID == "" {
		id, err := newTaskID()
		if err != nil {
			return Task{}, fmt.Errorf("%w: task id: %w", ErrConfig, err)
		}
		req.TaskID = id
	}
	return c.taskCall(ctx, peer, "task/send", &req)
}

// Get returns one task snapshot.
func (c *Client) Get(ctx context.Context, peer, id string) (Task, error) {
	return c.taskCall(ctx, peer, "task/get", map[string]string{"id": id})
}

// Status is the read-only task state query.
func (c *Client) Status(ctx context.Context, peer, id string) (Task, error) {
	return c.Get(ctx, peer, id)
}

// Cancel asks the peer to cancel a task.
func (c *Client) Cancel(ctx context.Context, peer, id string) (Task, error) {
	return c.taskCall(ctx, peer, "task/cancel", map[string]string{"id": id})
}

// taskCall runs one method and decodes its task result.
func (c *Client) taskCall(ctx context.Context, peer, method string, params any) (Task, error) {
	var raw json.RawMessage
	if err := c.call(ctx, peer, method, params, &raw); err != nil {
		return Task{}, err
	}
	task, err := UnmarshalTask(raw)
	if err != nil {
		return Task{}, err
	}
	c.record(peer, &task)
	return task, nil
}

// Wait polls a task until it is terminal or the bounded wait expires. It uses
// a ticker, never an inline sleep.
func (c *Client) Wait(ctx context.Context, peer, id string, limit time.Duration) (Task, error) {
	if limit <= 0 {
		limit = c.timeout
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		task, err := c.Get(ctx, peer, id)
		if err != nil {
			return Task{}, err
		}
		if task.State.Terminal() {
			return task, nil
		}
		select {
		case <-ctx.Done():
			return task, fmt.Errorf("a2a: wait %s: %w", id, ctx.Err())
		case <-ticker.C:
		}
	}
}

// Stream submits a task and reads the peer's SSE updates until the task is
// terminal or the stream ends. Every decoded snapshot is handed to fn.
func (c *Client) Stream(ctx context.Context, peer string, req Request, fn func(Task) error) error {
	if fn == nil {
		return fmt.Errorf("%w: stream callback", ErrConfig)
	}
	if req.TaskID == "" {
		id, err := newTaskID()
		if err != nil {
			return fmt.Errorf("%w: task id: %w", ErrConfig, err)
		}
		req.TaskID = id
	}
	p, ok := c.peers[peer]
	if !ok {
		return fmt.Errorf("%w: %q", ErrNoPeer, peer)
	}
	if _, serr := c.Send(ctx, peer, req); serr != nil {
		return serr
	}
	resp, err := c.openStream(ctx, p, req.TaskID)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			return
		}
	}()
	return c.readStream(resp.Body, peer, fn)
}

func (c *Client) openStream(ctx context.Context, p Peer, id string) (*http.Response, error) {
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage("1"), Method: "task/stream", Params: mustParams(map[string]string{"id": id})})
	if err != nil {
		return nil, fmt.Errorf("a2a: stream request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("a2a: stream request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("a2a: stream %s: %w", p.Name, err)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		if cerr := resp.Body.Close(); cerr != nil {
			return nil, fmt.Errorf("a2a: stream %s: %w", p.Name, cerr)
		}
		return nil, ErrAuth
	case http.StatusOK:
		return resp, nil
	default:
		if cerr := resp.Body.Close(); cerr != nil {
			return nil, fmt.Errorf("a2a: stream %s: %w", p.Name, cerr)
		}
		return nil, fmt.Errorf("a2a: stream %s: status %d", p.Name, resp.StatusCode)
	}
}

func (c *Client) readStream(body io.Reader, peer string, fn func(Task) error) error {
	parser := netx.NewSSEParser(body, MaxCardBytes)
	for {
		event, err := parser.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("a2a: stream %s: %w", peer, err)
		}
		task, err := decodeStreamEvent(event.Data)
		if err != nil {
			return err
		}
		c.record(peer, &task)
		if err := fn(task); err != nil {
			return err
		}
		if task.State.Terminal() {
			return nil
		}
	}
}

func decodeStreamEvent(data []byte) (Task, error) {
	var raw json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return Task{}, fmt.Errorf("%w: stream frame", ErrBadResponse)
	}
	return UnmarshalTask(raw)
}

// Discover fetches and validates the agent card of a peer.
func (c *Client) Discover(ctx context.Context, peer string) (AgentCard, error) {
	p, ok := c.peers[peer]
	if !ok {
		return AgentCard{}, fmt.Errorf("%w: %q", ErrNoPeer, peer)
	}
	cardURL := strings.TrimSuffix(p.URL, "/") + CardPath
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cardURL, nil)
	if err != nil {
		return AgentCard{}, fmt.Errorf("a2a: card request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return AgentCard{}, fmt.Errorf("a2a: card %s: %w", peer, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			return
		}
	}()
	return decodeCard(resp)
}

func decodeCard(resp *http.Response) (AgentCard, error) {
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusServiceUnavailable {
		return AgentCard{}, ErrCardAbsent
	}
	if resp.StatusCode != http.StatusOK {
		return AgentCard{}, fmt.Errorf("a2a: card: status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxCardBytes))
	if err != nil {
		return AgentCard{}, fmt.Errorf("a2a: card: %w", err)
	}
	var card AgentCard
	if err := json.Unmarshal(raw, &card); err != nil {
		return AgentCard{}, fmt.Errorf("%w: json", ErrInvalidCard)
	}
	if err := card.Validate(); err != nil {
		return AgentCard{}, err
	}
	return card, nil
}

func (c *Client) call(ctx context.Context, peer, method string, params, out any) error {
	p, ok := c.peers[peer]
	if !ok {
		return fmt.Errorf("%w: %q", ErrNoPeer, peer)
	}
	if err := ValidatePeerURL(p.URL, c.allow); err != nil {
		return err
	}
	id := c.nextID()
	body, err := json.Marshal(rpcRequest{JSONRPC: "2.0", ID: json.RawMessage(strconv.Quote(id)), Method: method, Params: mustParams(params)})
	if err != nil {
		return fmt.Errorf("a2a: %s request: %w", method, err)
	}
	// The task id is generated before the first attempt, so a retried send
	// cannot execute the peer's task twice.
	err = retry.Do(ctx, c.policy, func(attemptCtx context.Context, _ int) error {
		return c.attempt(attemptCtx, p, method, body, out)
	})
	if err != nil {
		return fmt.Errorf("a2a: %s %s: %w", method, peer, err)
	}
	return nil
}

func (c *Client) attempt(ctx context.Context, p Peer, method string, body []byte, out any) error {
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, p.URL, bytes.NewReader(body))
	if err != nil {
		return &retry.Error{Class: retry.BadRequest, Attempts: 1, Err: err}
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("a2a: %s %s: %w", method, p.Name, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			return
		}
	}()
	if class := classifyStatus(resp.StatusCode); class != retry.None {
		return &retry.Error{Class: class, Attempts: 1, Err: statusError(resp.StatusCode)}
	}
	return decodeRPCResponse(resp.Body, out)
}

func decodeRPCResponse(body io.Reader, out any) error {
	var doc struct {
		JSONRPC string          `json:"jsonrpc"`
		Result  json.RawMessage `json:"result"`
		Error   *RPCError       `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(body, MaxTaskText+MaxCardBytes)).Decode(&doc); err != nil {
		return &retry.Error{Class: retry.Server, Attempts: 1, Err: fmt.Errorf("%w: decode", ErrBadResponse)}
	}
	if doc.Error != nil {
		return doc.Error
	}
	if len(doc.Result) == 0 {
		return &retry.Error{Class: retry.BadRequest, Attempts: 1, Err: ErrBadResponse}
	}
	if out == nil {
		return nil
	}
	if raw, ok := out.(*json.RawMessage); ok {
		*raw = slices.Clone(doc.Result)
		return nil
	}
	if err := json.Unmarshal(doc.Result, out); err != nil {
		return &retry.Error{Class: retry.BadRequest, Attempts: 1, Err: fmt.Errorf("%w: result: %w", ErrBadResponse, err)}
	}
	return nil
}

func classifyStatus(status int) retry.Class {
	switch {
	case status == http.StatusTooManyRequests:
		return retry.RateLimited
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return retry.Auth
	case status >= 500:
		return retry.Server
	case status >= 300:
		return retry.BadRequest
	default:
		return retry.None
	}
}

func statusError(status int) error {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrAuth
	default:
		return fmt.Errorf("a2a: status %d", status)
	}
}

func mustParams(v any) json.RawMessage {
	raw, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return raw
}

func (c *Client) nextID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", c.now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func (c *Client) record(peer string, task *Task) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hist = append(c.hist, History{Peer: peer, TaskID: task.ID, State: task.State, At: c.now()})
	if len(c.hist) > c.history {
		c.hist = c.hist[len(c.hist)-c.history:]
	}
}
