package gwwa

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Stdio returns the newline-delimited JSON-RPC transport over a private pipe
// (bridge spawned as a child, SPEC §9.7.1). The caller owns the process.
func Stdio(rw io.ReadWriteCloser) Transport {
	t := &stdioTransport{
		rw:    rw,
		notif: make(chan []byte, 32),
		resp:  make(chan []byte, 4),
		done:  make(chan struct{}),
	}
	go t.read()
	return t
}

type stdioTransport struct {
	rw      io.ReadWriteCloser
	writeMu sync.Mutex
	notif   chan []byte
	resp    chan []byte
	done    chan struct{}
}

// RoundTrip writes one frame and waits for the response. Notifications
// interleaved on the same pipe are routed to the event stream.
func (t *stdioTransport) RoundTrip(ctx context.Context, frame []byte) ([]byte, error) {
	t.writeMu.Lock()
	_, err := t.rw.Write(append(frame, '\n'))
	t.writeMu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("gwwa: write: %w", err)
	}
	select {
	case raw := <-t.resp:
		return raw, nil
	case <-t.done:
		return nil, ErrClosed
	case <-ctx.Done():
		return nil, fmt.Errorf("gwwa: response: %w", ctx.Err())
	}
}

// Notifications implements Transport.
func (t *stdioTransport) Notifications(context.Context) (<-chan []byte, error) {
	return t.notif, nil
}

// Close implements Transport.
func (t *stdioTransport) Close() error {
	if err := t.rw.Close(); err != nil {
		return fmt.Errorf("gwwa: close: %w", err)
	}
	return nil
}

// read classifies every line as a response (has `id`) or a notification (has
// `method`) and never buffers more than one MaxFrame.
func (t *stdioTransport) read() {
	defer close(t.notif)
	defer close(t.done)
	reader := bufio.NewReaderSize(t.rw, 64<<10)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > MaxFrame {
			return
		}
		if frame := bytes.TrimRight(line, "\r\n"); len(frame) > 0 {
			t.route(frame)
		}
		if err != nil {
			return
		}
	}
}

func (t *stdioTransport) route(frame []byte) {
	var probe struct {
		ID     *uint64 `json:"id"`
		Method string  `json:"method"`
	}
	if err := json.Unmarshal(frame, &probe); err != nil {
		return
	}
	switch {
	case probe.Method != "":
		select {
		case t.notif <- frame:
		default:
		}
	case probe.ID != nil:
		select {
		case t.resp <- frame:
		default:
		}
	}
}
