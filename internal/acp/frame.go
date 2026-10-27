package acp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// MaxFrameBytes is the hard per-frame cap of SPEC §13.3 (16 MiB payload,
// excluding the terminating LF).
const MaxFrameBytes = 16 << 20

// DefaultWriteTimeout is the per-frame stdout deadline of SPEC §13.3.
const DefaultWriteTimeout = 10 * time.Second

// ErrFrameTooLarge reports a frame whose payload exceeds the cap. The reader
// drains the rest of the line before returning it, so the next call starts at
// a frame boundary.
var ErrFrameTooLarge = errors.New("acp: frame exceeds the 16 MiB cap")

// Reader splits NDJSON frames on LF only. U+2028 and U+2029 are ordinary
// string bytes, never separators (EC-FIO-018).
type Reader struct {
	br  *bufio.Reader
	max int
}

// NewReader returns a Reader with the SPEC §13.3 cap.
func NewReader(r io.Reader) *Reader { return NewReaderLimit(r, MaxFrameBytes) }

// NewReaderLimit overrides the payload cap (tests). max <= 0 keeps the
// default.
func NewReaderLimit(r io.Reader, max int) *Reader {
	if max <= 0 {
		max = MaxFrameBytes
	}
	return &Reader{br: bufio.NewReaderSize(r, 64<<10), max: max}
}

// ReadFrame returns the next payload (without the terminating LF). It reads
// incrementally: memory stays bounded by the cap plus one bufio chunk even
// when the peer streams a 100 MB line. A blank line is returned as an empty
// payload; the connection loop skips it.
func (r *Reader) ReadFrame() ([]byte, error) {
	acc := &accumulator{max: r.max}
	for {
		chunk, err := r.br.ReadSlice('\n')
		acc.add(chunk)
		switch {
		case err == nil:
			if acc.over {
				return nil, ErrFrameTooLarge
			}
			return trimLF(acc.buf), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if acc.over {
				return nil, ErrFrameTooLarge
			}
			if len(acc.buf) == 0 {
				return nil, io.EOF
			}
			return trimLF(acc.buf), nil
		default:
			return nil, fmt.Errorf("acp: read frame: %w", err)
		}
	}
}

// accumulator keeps at most max bytes; over-cap frames are only drained.
type accumulator struct {
	buf     []byte
	payload int
	max     int
	over    bool
}

func (a *accumulator) add(chunk []byte) {
	if a.over {
		return
	}
	n := len(chunk)
	if n > 0 && chunk[n-1] == '\n' {
		n--
	}
	a.payload += n
	if a.payload > a.max {
		a.over = true
		a.buf = nil
		return
	}
	a.buf = append(a.buf, chunk...)
}

func trimLF(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] == '\n' {
		return b[:len(b)-1]
	}
	return b
}

// FrameWriter writes one complete wire frame. Implementations must honour the
// context deadline; FileWriter maps it to the OS write deadline.
type FrameWriter interface {
	WriteFrame(ctx context.Context, frame []byte) error
}

// FileWriter writes frames to an *os.File (stdout or a test pipe).
type FileWriter struct{ F *os.File }

// WriteFrame writes frame with the context deadline as the OS write deadline.
// A pipe or socket supports deadlines; a regular file does not, and the write
// then proceeds without one (the caller's context still bounds the call).
func (w FileWriter) WriteFrame(ctx context.Context, frame []byte) error {
	if w.F == nil {
		return errors.New("acp: nil writer")
	}
	if dl, ok := ctx.Deadline(); ok {
		if err := w.F.SetWriteDeadline(dl); err != nil && !errors.Is(err, os.ErrNoDeadline) {
			return fmt.Errorf("acp: set write deadline: %w", err)
		}
	}
	for len(frame) > 0 {
		n, err := w.F.Write(frame)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return fmt.Errorf("%w: %w", ErrWriteTimeout, err)
			}
			return fmt.Errorf("acp: write frame: %w", err)
		}
		frame = frame[n:]
	}
	// Clear the deadline so later writes are not rejected by a stale one.
	if derr := w.F.SetWriteDeadline(time.Time{}); derr != nil && !errors.Is(derr, os.ErrNoDeadline) {
		return fmt.Errorf("acp: clear write deadline: %w", derr)
	}
	return nil
}

// Conn couples a reader and writer with the write deadline; it has no
// goroutines.
type Conn struct {
	r            *Reader
	w            FrameWriter
	WriteTimeout time.Duration
}

// NewConn returns a connection over r and w. A zero timeout takes
// DefaultWriteTimeout.
func NewConn(r io.Reader, w FrameWriter) *Conn {
	return &Conn{r: NewReader(r), w: w, WriteTimeout: DefaultWriteTimeout}
}

// Next returns the next request. A blank line is skipped. Transport failures
// (EOF, I/O) are returned as err; protocol failures are returned as a *Error
// that the caller answers with a null id. An oversized frame yields
// CodeInvalidRequest (-32600), never a transport error (SPEC §13.3).
func (c *Conn) Next(ctx context.Context) (Request, *Error, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Request{}, nil, fmt.Errorf("acp: next: %w", err)
		}
		frame, err := c.r.ReadFrame()
		switch {
		case errors.Is(err, ErrFrameTooLarge):
			return Request{}, &Error{Code: CodeInvalidRequest, Message: "frame exceeds 16 MiB"}, nil
		case err != nil:
			return Request{}, nil, fmt.Errorf("acp: next: %w", err)
		}
		if isBlank(frame) {
			continue
		}
		req, rpcErr := ParseRequest(frame)
		return req, rpcErr, nil
	}
}

func isBlank(b []byte) bool {
	for _, c := range b {
		if c != ' ' && c != '\t' && c != '\r' {
			return false
		}
	}
	return true
}

// WriteResponse writes one response frame with the write deadline.
func (c *Conn) WriteResponse(ctx context.Context, resp *Response) error {
	frame, err := EncodeResponse(resp)
	if err != nil {
		return err
	}
	return c.write(ctx, frame)
}

func (c *Conn) write(ctx context.Context, frame []byte) error {
	if c.w == nil {
		return errors.New("acp: nil frame writer")
	}
	timeout := c.WriteTimeout
	if timeout <= 0 {
		timeout = DefaultWriteTimeout
	}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := c.w.WriteFrame(wctx, frame)
	if err != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrWriteTimeout)) {
		return ErrWriteTimeout
	}
	if err != nil {
		return fmt.Errorf("acp: write frame: %w", err)
	}
	return nil
}
