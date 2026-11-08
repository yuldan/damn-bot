package gwwa

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Transport is one duplex connection to the bridge: JSON-RPC request/response
// plus the notification stream. Both the stdio pipe and the loopback HTTP
// transport implement it.
type Transport interface {
	// RoundTrip writes one request frame and returns the response frame.
	RoundTrip(ctx context.Context, frame []byte) ([]byte, error)
	// Notifications returns the notification frame stream. The channel closes
	// when the transport ends.
	Notifications(ctx context.Context) (<-chan []byte, error)
	// Close releases the transport.
	Close() error
}

// Client is one `damnbot.bridge/v1` session. Calls are serialized; the
// notification pump is the only reader of the event stream.
type Client struct {
	tr    Transport
	guard *Guard

	hello  Hello
	events chan Event
	done   chan struct{}
	err    error

	// own is the single ownership token of the session. A call takes it
	// before it writes a frame and holds it until the response body is read
	// or the context ends, so a slow request can never grow a second owner.
	own       chan struct{}
	next      atomic.Uint64
	closeOnce sync.Once
}

// request is one JSON-RPC 2.0 request frame.
type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// response is one JSON-RPC 2.0 response frame.
type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// notification is one JSON-RPC 2.0 notification frame.
type notification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Connect performs the `bridge.hello` handshake over tr and returns a client.
// A missing, wrong-major or unpinned hello is `bridge_incompatible`.
func Connect(ctx context.Context, tr Transport) (*Client, error) {
	if tr == nil {
		return nil, fmt.Errorf("%w: nil transport", ErrProtocol)
	}
	stream, err := tr.Notifications(ctx)
	if err != nil {
		return nil, fmt.Errorf("gwwa: notifications: %w", err)
	}
	var raw []byte
	var ok bool
	select {
	case raw, ok = <-stream:
	case <-ctx.Done():
		return nil, closeWith(tr, fmt.Errorf("gwwa: handshake: %w", ctx.Err()))
	}
	if !ok {
		return nil, ErrNoHandshake
	}
	var n notification
	if err := json.Unmarshal(raw, &n); err != nil || n.Method != "bridge.hello" {
		return nil, closeWith(tr, ErrNoHandshake)
	}
	var h Hello
	if err := json.Unmarshal(n.Params, &h); err != nil {
		return nil, closeWith(tr, fmt.Errorf("%w: decode hello: %w", ErrProtocol, err))
	}
	if err := h.Validate(); err != nil {
		return nil, closeWith(tr, err)
	}
	c := &Client{
		tr:     tr,
		guard:  NewGuard(0),
		hello:  h,
		events: make(chan Event, 32),
		done:   make(chan struct{}),
		own:    make(chan struct{}, 1),
	}
	go c.pump(ctx, stream)
	return c, nil
}

// Hello returns the validated handshake.
func (c *Client) Hello() Hello { return c.hello }

// Events is the decoded bridge notification stream.
func (c *Client) Events() <-chan Event { return c.events }

// Send transmits one text message. A chat without inbound history is refused
// locally with ErrColdOutbound and writes no frame.
func (c *Client) Send(ctx context.Context, chat, text, idemKey string) error {
	if chat == "" || text == "" || idemKey == "" {
		return fmt.Errorf("%w: send requires chat, text and idem_key", ErrProtocol)
	}
	if !c.guard.Allow(chat) {
		return fmt.Errorf("%w: %s", ErrColdOutbound, chat)
	}
	return c.Call(ctx, "send", map[string]any{"chat_id": chat, "text": text, "idem_key": idemKey}, nil)
}

// Ack acknowledges one delivered external id.
func (c *Client) Ack(ctx context.Context, externalID string) error {
	return c.Call(ctx, "ack", map[string]any{"external_id": externalID}, nil)
}

// Status queries the bridge.
func (c *Client) Status(ctx context.Context) (Health, error) {
	var h Health
	if err := c.Call(ctx, "status", struct{}{}, &h); err != nil {
		return Health{}, err
	}
	return h, nil
}

// Logout asks the bridge to log the WhatsApp Web session out.
func (c *Client) Logout(ctx context.Context) error {
	return c.Call(ctx, "logout", struct{}{}, nil)
}

// Call performs one JSON-RPC request.
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	select {
	case <-c.done:
		return fmt.Errorf("%w: %w", ErrClosed, c.err)
	default:
	}
	select {
	case c.own <- struct{}{}:
		defer func() { <-c.own }()
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", ErrOwned, ctx.Err())
	}
	frame, err := json.Marshal(request{JSONRPC: "2.0", ID: c.next.Add(1), Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("gwwa: encode %s: %w", method, err)
	}
	raw, err := c.tr.RoundTrip(ctx, frame)
	if err != nil {
		return fmt.Errorf("gwwa: %s: %w", method, err)
	}
	var resp response
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("gwwa: decode %s: %w", method, err)
	}
	if resp.Error != nil {
		if resp.Error.IsColdOutbound() {
			return fmt.Errorf("%w: %w", ErrColdOutbound, resp.Error)
		}
		return resp.Error
	}
	if out == nil || len(resp.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Result, out); err != nil {
		return fmt.Errorf("gwwa: decode %s result: %w", method, err)
	}
	return nil
}

// Close ends the session once.
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		if cerr := c.tr.Close(); cerr != nil {
			err = fmt.Errorf("gwwa: close: %w", cerr)
		}
		<-c.done
	})
	return err
}

// closeWith closes a half-open transport while preserving the handshake error.
func closeWith(tr Transport, err error) error {
	if cerr := tr.Close(); cerr != nil {
		return errors.Join(err, fmt.Errorf("gwwa: close: %w", cerr))
	}
	return err
}

// pump decodes notifications until the transport ends. A malformed
// notification ends the session; the adapter reconnects with backoff.
func (c *Client) pump(ctx context.Context, stream <-chan []byte) {
	defer close(c.events)
	defer close(c.done)
	for raw := range stream {
		var n notification
		if err := json.Unmarshal(raw, &n); err != nil {
			c.err = fmt.Errorf("gwwa: decode notification: %w", err)
			return
		}
		if n.Method == "bridge.hello" {
			continue
		}
		ev, err := decodeEvent(n)
		if err != nil {
			c.err = err
			return
		}
		if ev.Inbound != nil {
			c.guard.Note(ev.Inbound.ChatID, time.Now())
		}
		select {
		case c.events <- ev:
		case <-ctx.Done():
			return
		}
	}
}

func decodeEvent(n notification) (Event, error) {
	ev := Event{Method: n.Method}
	switch n.Method {
	case "inbound":
		var in Inbound
		if err := json.Unmarshal(n.Params, &in); err != nil {
			return ev, fmt.Errorf("gwwa: decode inbound: %w", err)
		}
		if in.ExternalID == "" || in.ChatID == "" || in.SenderID == "" {
			return ev, fmt.Errorf("%w: inbound ids", ErrProtocol)
		}
		ev.Inbound = &in
	case "health":
		var h Health
		if err := json.Unmarshal(n.Params, &h); err != nil {
			return ev, fmt.Errorf("gwwa: decode health: %w", err)
		}
		ev.Health = &h
	}
	return ev, nil
}
