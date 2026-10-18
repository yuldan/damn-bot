package netx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"time"
)

// SSE limits (ADR-100).
const (
	sseInitialBuf = 4 << 10
	bom           = "\xEF\xBB\xBF"
	// DefaultMaxEvent bounds one line and one event's joined data; larger
	// input fails with ErrEventTooLarge instead of growing without limit.
	DefaultMaxEvent = 4 << 20
)

// ErrEventTooLarge is returned when a line or an event's data exceeds the
// parser limit.
var ErrEventTooLarge = errors.New("sse: event too large")

// Event is one dispatched server-sent event. Its slices alias parser
// buffers and stay valid only until the next call to [SSEParser.Next].
type Event struct {
	Type []byte // "event" field; empty = "message"
	Data []byte // "data" lines joined with LF, final LF removed
	ID   []byte // last event ID (persists across events, WHATWG §9.2.6)
}

// SSEParser frames text/event-stream (WHATWG HTML §9.2.6): LF, CR or CRLF
// line endings, an optional leading BOM, comments, and the data, event,
// id and retry fields. After warmup it allocates nothing per event (E-35).
// Comments (keepalives) never surface as events, so a stream watchdog reset
// per event ignores them (EC-PROV-009).
type SSEParser struct {
	r       io.Reader
	max     int
	buf     []byte // unread input is buf[off:end]
	off     int
	end     int
	data    []byte
	typ     []byte
	id      []byte
	retry   time.Duration
	started bool // BOM check done (only at stream start)
	skipLF  bool // previous line ended in CR at a buffer boundary
	err     error
}

// NewSSEParser reads events from r; maxEvent ≤ 0 means DefaultMaxEvent.
func NewSSEParser(r io.Reader, maxEvent int) *SSEParser {
	if maxEvent <= 0 {
		maxEvent = DefaultMaxEvent
	}
	return &SSEParser{r: r, max: maxEvent, buf: make([]byte, sseInitialBuf), retry: -1}
}

// Retry returns the last valid "retry" field value; ok is false if none.
func (p *SSEParser) Retry() (d time.Duration, ok bool) { return p.retry, p.retry >= 0 }

// Next returns the next event. At the end of input it returns io.EOF; a
// trailing event without its blank line is discarded (WHATWG). Read errors
// and ErrEventTooLarge are sticky.
func (p *SSEParser) Next() (Event, error) {
	for {
		line, err := p.line()
		if err != nil {
			return Event{}, err
		}
		if len(line) == 0 {
			if len(p.data) == 0 {
				p.typ = p.typ[:0]
				continue
			}
			ev := Event{Type: p.typ, Data: p.data[:len(p.data)-1], ID: p.id}
			p.data, p.typ = p.data[:0], p.typ[:0]
			return ev, nil
		}
		if err := p.field(line); err != nil {
			p.err = err
			return Event{}, err
		}
	}
}

// field applies one non-empty line.
func (p *SSEParser) field(line []byte) error {
	name, value := line, []byte(nil)
	if i := bytes.IndexByte(line, ':'); i >= 0 {
		if i == 0 {
			return nil // comment
		}
		name, value = line[:i], line[i+1:]
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
	}
	switch string(name) { // no allocation: compiler-optimised comparison
	case "data":
		if len(p.data)+len(value)+1 > p.max {
			return ErrEventTooLarge
		}
		p.data = append(append(p.data, value...), '\n')
	case "event":
		p.typ = append(p.typ[:0], value...)
	case "id":
		if bytes.IndexByte(value, 0) < 0 {
			p.id = append(p.id[:0], value...)
		}
	case "retry":
		p.setRetry(value)
	}
	return nil
}

func (p *SSEParser) setRetry(v []byte) {
	if len(v) == 0 || len(v) > 12 {
		return
	}
	var ms int64
	for _, c := range v {
		if c < '0' || c > '9' {
			return
		}
		ms = ms*10 + int64(c-'0')
	}
	p.retry = time.Duration(ms) * time.Millisecond
}

// line returns the next line without its terminator; it aliases buf.
func (p *SSEParser) line() ([]byte, error) {
	for p.err == nil {
		if !p.prologue() {
			chunk := p.buf[p.off:p.end]
			if i := bytes.IndexAny(chunk, "\r\n"); i >= 0 {
				p.off += i + 1
				p.skipLF = chunk[i] == '\r'
				return chunk[:i], nil
			}
		}
		p.err = p.fill()
	}
	return nil, p.err
}

// prologue drops the LF of a CRLF split across reads and, once at stream
// start, the BOM; it reports whether more input is needed first.
func (p *SSEParser) prologue() bool {
	if p.skipLF && p.off < p.end {
		p.skipLF = false
		if p.buf[p.off] == '\n' {
			p.off++
		}
	}
	if p.started {
		return false
	}
	avail := p.buf[p.off:p.end]
	if len(avail) < len(bom) && bytes.HasPrefix([]byte(bom), avail) {
		return true
	}
	p.started = true
	if bytes.HasPrefix(avail, []byte(bom)) {
		p.off += len(bom)
	}
	return false
}

// fill reads more input, compacting or growing buf as needed.
func (p *SSEParser) fill() error {
	if p.off > 0 {
		p.end = copy(p.buf, p.buf[p.off:p.end])
		p.off = 0
	}
	if p.end == len(p.buf) {
		if len(p.buf) >= p.max {
			return ErrEventTooLarge
		}
		nb := make([]byte, min(2*len(p.buf), p.max))
		copy(nb, p.buf[:p.end])
		p.buf = nb
	}
	n, err := p.r.Read(p.buf[p.end:])
	p.end += n
	switch {
	case n > 0:
		return nil
	case errors.Is(err, io.EOF):
		return io.EOF
	case err != nil:
		return fmt.Errorf("sse read: %w", err)
	}
	return nil // 0, nil: try again
}
