package jsonrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"sync/atomic"
)

// Handler serves one inbound request or notification; the context is canceled
// when the connection closes.
type (
	Handler    func(ctx context.Context, method string, params json.RawMessage) (any, *Error)
	callResult struct {
		message Message
		err     error
	}
)

// Conn is a bidirectional JSON-RPC stream; one goroutine reads the transport.
type Conn struct {
	reader   *FrameReader
	writer   *FrameWriter
	opts     Options
	closeFn  func()
	seq      atomic.Uint64
	handlers sync.WaitGroup
	mu       sync.Mutex
	pending  map[string]chan callResult
	incoming map[string]bool
	terminal error
	once     sync.Once
	ctx      context.Context //nolint:containedctx // Close owns handler cancellation.
	cancel   context.CancelFunc
}

// NewConn starts a stream over r and w; closeFn, when non-nil, unblocks Close.
func NewConn(r io.Reader, w io.Writer, closeFn func(), opts Options) *Conn {
	ctx, cancel := context.WithCancel(context.Background())
	c := &Conn{
		reader:   NewFrameReader(r, opts),
		writer:   NewFrameWriter(w, opts),
		opts:     opts,
		closeFn:  closeFn,
		pending:  make(map[string]chan callResult),
		incoming: make(map[string]bool),
		ctx:      ctx,
		cancel:   cancel,
	}
	c.handlers.Add(1)
	go c.readLoop()
	return c
}

// Close fails outstanding calls, stops the reader, and waits for handlers.
func (c *Conn) Close() error {
	c.shutdown(ErrClosed)
	c.handlers.Wait()
	return nil
}

// Call sends one request and waits for its matching response.
func (c *Conn) Call(ctx context.Context, method string, params, result any) error {
	id := json.RawMessage(strconv.FormatUint(c.seq.Add(1), 10))
	msg, err := newMessage(method, params, id)
	if err != nil {
		return err
	}
	key := idKey(id)
	ch := make(chan callResult, 1)
	c.mu.Lock()
	c.pending[key] = ch
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, key); c.mu.Unlock() }()
	if err := c.write(&msg); err != nil {
		return err
	}
	select {
	case out := <-ch:
		if out.err != nil {
			return out.err
		}
		return decodeResponse(&out.message, result)
	case <-ctx.Done():
		if c.opts.NotifyCancel {
			if nerr := c.notifyCancel(ctx, id); nerr != nil {
				return errors.Join(ctx.Err(), nerr)
			}
		}
		return fmt.Errorf("jsonrpc call %q: %w", method, ctx.Err())
	}
}

// notifyCancel sends the cancellation notification for id. The zero options
// keep the LSP default; MCP overrides both the method and the id field.
func (c *Conn) notifyCancel(ctx context.Context, id json.RawMessage) error {
	method, param := c.opts.CancelMethod, c.opts.CancelParam
	if method == "" {
		method = CancelMethod
	}
	if param == "" {
		param = "id"
	}
	return c.Notify(context.WithoutCancel(ctx), method, map[string]json.RawMessage{param: id})
}

// Notify sends one notification and does not wait for a reply.
func (c *Conn) Notify(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("jsonrpc notify %q: %w", method, err)
	}
	msg, err := newMessage(method, params, nil)
	if err != nil {
		return err
	}
	return c.write(&msg)
}

func (c *Conn) write(m *Message) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("jsonrpc: marshal message: %w", err)
	}
	c.mu.Lock()
	terminal := c.terminal
	c.mu.Unlock()
	if terminal != nil {
		return terminal
	}
	if err := c.writer.WriteFrame(raw); err != nil {
		wrapped := fmt.Errorf("%w: %w", ErrPeerClosed, err)
		c.shutdown(wrapped)
		return wrapped
	}
	return nil
}

func (c *Conn) readLoop() {
	defer c.handlers.Done()
	for {
		frame, err := c.reader.ReadFrame()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = fmt.Errorf("%w: %w", ErrPeerClosed, io.EOF)
			} else {
				err = fmt.Errorf("%w: %w", ErrInvalidFrame, err)
			}
			c.shutdown(err)
			return
		}
		c.handle(frame)
	}
}

func (c *Conn) handle(frame []byte) {
	trimmed := bytes.TrimSpace(frame)
	if len(trimmed) == 0 {
		return
	}
	if trimmed[0] == '[' {
		c.sendJSON(&Message{JSONRPC: Version, ID: json.RawMessage("null"), Error: &Error{Code: CodeInvalidRequest, Message: "batch not supported"}})
		return
	}
	var m Message
	if err := json.Unmarshal(trimmed, &m); err != nil {
		c.sendJSON(&Message{JSONRPC: Version, ID: json.RawMessage("null"), Error: &Error{Code: CodeParseError, Message: "parse error"}})
		return
	}
	if m.JSONRPC != Version {
		if m.hasID() {
			c.sendJSON(&Message{JSONRPC: Version, ID: m.ID, Error: &Error{Code: CodeInvalidRequest, Message: "invalid request"}})
		}
		return
	}
	if m.Method == "" {
		c.deliver(&m)
		return
	}
	c.dispatch(&m, m.hasID())
}

func methodNotFound(context.Context, string, json.RawMessage) (any, *Error) {
	return nil, &Error{Code: CodeMethodNotFound, Message: "method not found"}
}

func (c *Conn) dispatch(m *Message, request bool) {
	key := idKey(m.ID)
	if request {
		c.mu.Lock()
		duplicate := c.incoming[key]
		if !duplicate {
			c.incoming[key] = true
		}
		c.mu.Unlock()
		if duplicate {
			c.sendJSON(&Message{JSONRPC: Version, ID: m.ID, Error: &Error{Code: CodeInvalidRequest, Message: "duplicate request id"}})
			return
		}
	}
	handler := c.opts.Handler
	if handler == nil {
		handler = methodNotFound
	}
	c.handlers.Go(func() {
		if request {
			defer func() { c.mu.Lock(); delete(c.incoming, key); c.mu.Unlock() }()
		}
		out, rerr := handler(c.ctx, m.Method, m.Params)
		if !request {
			return
		}
		if rerr != nil {
			c.sendJSON(&Message{JSONRPC: Version, ID: m.ID, Error: rerr})
			return
		}
		raw, err := marshalRaw(out)
		if err != nil {
			c.sendJSON(&Message{JSONRPC: Version, ID: m.ID, Error: &Error{Code: CodeInternalError, Message: "internal error"}})
			return
		}
		c.sendJSON(&Message{JSONRPC: Version, ID: m.ID, Result: raw})
	})
}

func (c *Conn) deliver(m *Message) {
	key := idKey(m.ID)
	c.mu.Lock()
	ch, ok := c.pending[key]
	if ok {
		delete(c.pending, key)
	}
	c.mu.Unlock()
	if ok {
		ch <- callResult{message: *m}
	}
}

func (c *Conn) sendJSON(m *Message) {
	if err := c.write(m); err != nil {
		c.shutdown(err)
	}
}

func (c *Conn) shutdown(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.terminal = err
		pending := c.pending
		c.pending = make(map[string]chan callResult)
		c.mu.Unlock()
		c.cancel()
		if c.closeFn != nil {
			c.closeFn()
		}
		for _, ch := range pending {
			ch <- callResult{err: err}
		}
	})
}
