package exec

// Framed control connections (EC-WIN-015): one listener serves many clients.
// Each client owns a length-prefixed frame stream, writes on one connection are
// serialized by a per-connection mutex (a send waits while another write is in
// flight, which is the backpressure — there is no unbounded queue), reads run
// in the client's own goroutine, and a client that dies is torn down without
// touching its siblings or the accept loop.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
)

const (
	// DefaultMaxFrame caps one framed payload in bytes (1 MiB, the same
	// ceiling as MCP arguments/results).
	DefaultMaxFrame = 1 << 20
	frameHeaderSize = 4
)

var (
	// ErrFrameTooLarge reports a payload above the frame limit.
	ErrFrameTooLarge = errors.New("exec: frame exceeds limit")
	// ErrEmptyFrame reports a zero-length frame, which this protocol refuses.
	ErrEmptyFrame = errors.New("exec: empty frame")
)

// Framer is the wire format of a control connection: a 4-byte big-endian
// payload length followed by exactly that many payload bytes.
type Framer struct {
	MaxFrame int // 0 = DefaultMaxFrame
}

// limit is the frame ceiling; values outside (0, MaxInt32] fall back to the
// default, so the conversion is exact.
func (f Framer) limit() uint32 {
	if n := f.MaxFrame; n > 0 && n <= math.MaxInt32 {
		return uint32(n)
	}
	return DefaultMaxFrame
}

// Read reads one frame, refusing empty frames and payloads above the limit
// before allocating them.
func (f Framer) Read(r io.Reader) ([]byte, error) {
	var hdr [frameHeaderSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, fmt.Errorf("exec: read frame header: %w", err)
	}
	n := binary.BigEndian.Uint32(hdr[:])
	switch {
	case n == 0:
		return nil, ErrEmptyFrame
	case n > f.limit():
		return nil, fmt.Errorf("%w: %d > %d", ErrFrameTooLarge, n, f.limit())
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, fmt.Errorf("exec: read frame payload: %w", err)
	}
	return payload, nil
}

// Write writes one frame in a single Write call.
func (f Framer) Write(w io.Writer, payload []byte) error {
	frame, err := f.frame(payload)
	if err != nil {
		return err
	}
	if _, err := w.Write(frame); err != nil {
		return fmt.Errorf("exec: write frame: %w", err)
	}
	return nil
}

func (f Framer) frame(payload []byte) ([]byte, error) {
	n := len(payload)
	switch {
	case n == 0:
		return nil, ErrEmptyFrame
	case int64(n) > math.MaxUint32:
		return nil, fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, n)
	}
	if uint32(n) > f.limit() {
		return nil, fmt.Errorf("%w: %d > %d", ErrFrameTooLarge, n, f.limit())
	}
	frame := make([]byte, frameHeaderSize+n)
	binary.BigEndian.PutUint32(frame, uint32(n))
	copy(frame[frameHeaderSize:], payload)
	return frame, nil
}

// Conn is one client transport (a Windows pipe connection or an in-memory
// pipe in tests).
type Conn interface {
	io.ReadWriteCloser
}

// Listener accepts client transports. Any Accept error stops Serve (a closed
// listener is reported once ctx is done); per-client refusals surface on that
// client's Receive. Close only stops Accept: implementations report the
// outcome through Accept.
type Listener interface {
	Accept() (Conn, error)
	Close()
}

// Client is one connection served by Mux. Receive reads from the peer, Send
// writes to it; concurrent sends are serialized by the connection's writer
// mutex and a failed transport is reported once through Err.
type Client struct {
	framer    Framer
	conn      Conn
	writeMu   sync.Mutex
	closeOnce sync.Once
	mu        sync.Mutex
	err       error
}

// Err is the first transport error recorded for this client, if any.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

func (c *Client) setErr(err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	if c.err == nil {
		c.err = err
	}
	c.mu.Unlock()
}

// Receive reads the next frame of this client. A transport error already
// recorded (by Send or Close) is reported first.
func (c *Client) Receive() ([]byte, error) {
	if err := c.Err(); err != nil {
		return nil, err
	}
	msg, err := c.framer.Read(c.conn)
	if err != nil {
		return nil, fmt.Errorf("exec: receive: %w", err)
	}
	return msg, nil
}

// Send writes one frame. Concurrent calls are serialized: while one frame is
// being written (a peer that does not read stalls it) the next caller waits on
// the connection mutex instead of buffering, which bounds memory per client.
func (c *Client) Send(ctx context.Context, payload []byte) error {
	frame, err := c.framer.frame(payload)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("exec: send: %w", err)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.Err(); err != nil {
		return err
	}
	if _, err := c.conn.Write(frame); err != nil {
		wrapped := fmt.Errorf("exec: write: %w", err)
		c.setErr(wrapped)
		c.Close()
		return wrapped
	}
	return nil
}

// Close closes the transport once; a blocked Send or Receive returns and the
// failure (if any) is retained by Err.
func (c *Client) Close() {
	c.closeOnce.Do(func() {
		c.setErr(c.conn.Close())
	})
}

// Mux serves framed clients from one Listener.
type Mux struct {
	MaxFrame int // 0 = DefaultMaxFrame
}

// Serve accepts clients until ctx is done or a fatal Accept error occurs and
// runs handler for each client in its own goroutine (that goroutine is the
// client's reader). Closing the listener on ctx is Serve's job; a handler that
// returns, dies, or is refused affects only its own connection.
func (m *Mux) Serve(ctx context.Context, ln Listener, handler func(*Client)) error {
	framer := Framer{MaxFrame: m.MaxFrame}
	stop := context.AfterFunc(ctx, ln.Close)
	defer stop()
	var (
		mu      sync.Mutex
		clients = map[*Client]struct{}{}
		wg      sync.WaitGroup
	)
	defer func() {
		mu.Lock()
		for c := range clients {
			c.Close()
		}
		mu.Unlock()
		wg.Wait()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			switch {
			case ctx.Err() != nil:
				return nil
			default:
				return fmt.Errorf("exec: accept: %w", err)
			}
		}
		c := &Client{framer: framer, conn: conn}
		mu.Lock()
		clients[c] = struct{}{}
		mu.Unlock()
		wg.Go(func() {
			defer func() {
				mu.Lock()
				delete(clients, c)
				mu.Unlock()
				c.Close()
			}()
			handler(c)
		})
	}
}
