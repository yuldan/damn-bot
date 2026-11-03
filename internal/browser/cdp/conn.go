// Package cdp is the CDP transport of the F6 browser module (ADR-313).
package cdp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/damnbot/damnbot/internal/netx"
)

// Transport failures.
var (
	ErrClosed      = errors.New("cdp: connection closed")
	ErrTransport   = errors.New("cdp: transport error")
	ErrTimeout     = errors.New("cdp: call timed out")
	errReconnected = errors.New("cdp: reconnect limit reached")
)

// Event is one unsolicited protocol message.
type Event struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Conn is one page connection. Call and Events are safe for concurrent use.
type Conn struct {
	url     string
	timeout time.Duration
	client  *http.Client
	nextID  atomic.Int64
	events  chan Event
	closed  chan struct{}
	once    sync.Once
	wg      sync.WaitGroup
	mu      sync.Mutex
	ws      *websocket.Conn
	done    chan struct{}
	pending map[int64]chan reply
	retried bool
	err     error
}
type envelope struct {
	ID     int64           `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}
type reply struct {
	raw json.RawMessage
	err error
}

// Dial connects to a CDP page WebSocket with the proxy disabled.
func Dial(ctx context.Context, rawURL string, timeout time.Duration) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("%w: invalid WebSocket URL", ErrTransport)
	}
	timeout = cmp.Or(timeout, 10*time.Second)
	c := &Conn{
		url: u.String(), timeout: timeout,
		client:  netx.LoopbackClient(),
		pending: make(map[int64]chan reply), events: make(chan Event, 256),
		closed: make(chan struct{}),
	}
	if err := c.attach(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Conn) attach(ctx context.Context) error {
	ws, resp, err := websocket.Dial(ctx, c.url, &websocket.DialOptions{HTTPClient: c.client})
	if err != nil {
		if resp != nil {
			return errors.Join(fmt.Errorf("cdp: dial %s: %w", c.url, err), resp.Body.Close())
		}
		return fmt.Errorf("cdp: dial %s: %w", c.url, err)
	}
	ws.SetReadLimit(16 << 20)
	c.mu.Lock()
	select {
	case <-c.closed:
		c.mu.Unlock()
		return errors.Join(ErrClosed, ws.CloseNow())
	default:
	}
	done := make(chan struct{})
	c.ws, c.done, c.err = ws, done, nil
	c.wg.Add(1)
	c.mu.Unlock()
	go c.read(context.WithoutCancel(ctx), ws, done)
	return nil
}

func (c *Conn) read(ctx context.Context, ws *websocket.Conn, done chan struct{}) {
	defer c.wg.Done()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			c.fail(done, err)
			return
		}
		if err := c.dispatch(done, data); err != nil {
			c.fail(done, err)
			return
		}
	}
}

func (c *Conn) dispatch(done chan struct{}, data []byte) error {
	var msg envelope
	if err := json.Unmarshal(data, &msg); err != nil {
		return fmt.Errorf("%w: %w", ErrTransport, err)
	}
	if msg.ID != 0 {
		c.mu.Lock()
		ch := c.pending[msg.ID]
		delete(c.pending, msg.ID)
		c.mu.Unlock()
		if ch == nil {
			return nil
		}
		if len(msg.Error) != 0 {
			ch <- reply{err: fmt.Errorf("cdp: %s", msg.Error)}
		} else {
			ch <- reply{raw: msg.Result}
		}
		return nil
	}
	if msg.Method == "" {
		return nil
	}
	select {
	case c.events <- Event{Method: msg.Method, Params: msg.Params}:
	case <-done:
	case <-c.closed:
	}
	return nil
}

// Call sends one method, waits for its reply and reconnects once.
func (c *Conn) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return nil, fmt.Errorf("%w: params: %w", ErrTransport, err)
		}
		raw = b
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	res, err := c.do(ctx, method, raw)
	if err != nil && errors.Is(err, ErrTransport) {
		if rerr := c.reconnect(ctx); rerr == nil {
			return c.do(ctx, method, raw)
		}
	}
	return res, err
}

func (c *Conn) do(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	ch := make(chan reply, 1)
	c.mu.Lock()
	if c.ws == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("%w: %s: %w", ErrTransport, method, ErrClosed)
	}
	ws, done := c.ws, c.done
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	payload, err := json.Marshal(envelope{ID: id, Method: method, Params: params})
	if err != nil {
		return nil, fmt.Errorf("%w: encode: %w", ErrTransport, err)
	}
	if err := ws.Write(ctx, websocket.MessageText, payload); err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrTransport, method, err)
	}
	select {
	case r := <-ch:
		return r.raw, r.err
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %s", ErrTimeout, method)
	case <-done:
		return nil, fmt.Errorf("%w: %s: session ended", ErrTransport, method)
	case <-c.closed:
		return nil, ErrClosed
	}
}

func (c *Conn) reconnect(ctx context.Context) error {
	c.mu.Lock()
	if c.retried || c.ws == nil {
		c.mu.Unlock()
		return errReconnected
	}
	c.retried = true
	ws, done := c.ws, c.done
	c.mu.Unlock()
	c.fail(done, errors.New("reconnecting"))
	if err := ws.CloseNow(); err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("cdp: reconnect: %w", err)
	}
	c.wg.Wait()
	select {
	case <-c.closed:
		return ErrClosed
	default:
		return c.attach(ctx)
	}
}

// Events returns events in arrival order; the channel closes on Close.
func (c *Conn) Events() <-chan Event { return c.events }

// Close closes the socket and waits for its reader.
func (c *Conn) Close() error {
	c.once.Do(func() {
		c.mu.Lock()
		close(c.closed)
		ws := c.ws
		c.ws = nil
		c.mu.Unlock()
		if ws != nil {
			if err := ws.Close(websocket.StatusNormalClosure, ""); err != nil && !errors.Is(err, net.ErrClosed) {
				c.mu.Lock()
				c.err = err
				c.mu.Unlock()
			}
		}
		c.wg.Wait()
		close(c.events)
	})
	return nil
}

func (c *Conn) fail(done chan struct{}, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done != done || c.err != nil {
		return
	}
	c.err = fmt.Errorf("%w: %w", ErrClosed, err)
	close(done)
	for id, ch := range c.pending {
		delete(c.pending, id)
		ch <- reply{err: c.err}
	}
}
