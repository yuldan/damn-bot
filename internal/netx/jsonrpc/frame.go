package jsonrpc

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"strconv"
	"sync"
)

// Options configures a stream connection or HTTP client; the zero value uses
// newline frames, a 1 MiB limit, and no cancellation notifications.
type Options struct {
	MaxFrame      int     // bytes per frame; zero means DefaultMaxFrame
	ContentLength bool    // select LSP Content-Length framing
	NotifyCancel  bool    // send [CancelMethod] when a call's context ends
	Handler       Handler // inbound requests; nil answers "method not found"
}

func (o Options) maxFrame() int {
	if o.MaxFrame > 0 {
		return o.MaxFrame
	}
	return DefaultMaxFrame
}

// FrameReader decodes one frame at a time; oversized newline frames are
// drained so the next call starts at the following frame boundary.
type FrameReader struct {
	r             *bufio.Reader
	max           int
	contentLength bool
}

// NewFrameReader builds a reader for opts; it owns no goroutine.
func NewFrameReader(r io.Reader, opts Options) *FrameReader {
	return &FrameReader{r: bufio.NewReader(r), max: opts.maxFrame(), contentLength: opts.ContentLength}
}

// ReadFrame returns the next JSON payload without its framing bytes.
func (f *FrameReader) ReadFrame() ([]byte, error) {
	if f.contentLength {
		return f.readContentLengthFrame()
	}
	return f.readNewlineFrame()
}

func (f *FrameReader) readNewlineFrame() ([]byte, error) {
	var payload []byte
	tooLarge := false
	for {
		chunk, err := f.r.ReadSlice('\n')
		if !tooLarge {
			if len(payload)+len(chunk) > f.max {
				tooLarge = true
				payload = nil
			} else {
				payload = append(payload, chunk...)
			}
		}
		switch {
		case err == nil:
			if tooLarge {
				return nil, ErrFrameTooLarge
			}
			return bytes.TrimRight(payload, "\r\n"), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(payload) == 0 && !tooLarge {
				return nil, io.EOF
			}
			if tooLarge {
				return nil, ErrFrameTooLarge
			}
			return bytes.TrimRight(payload, "\r\n"), nil
		default:
			return nil, fmt.Errorf("%w: %w", ErrInvalidFrame, err)
		}
	}
}

func (f *FrameReader) readContentLengthFrame() ([]byte, error) {
	header, err := textproto.NewReader(f.r).ReadMIMEHeader()
	if err != nil {
		return nil, fmt.Errorf("%w: headers: %w", ErrInvalidFrame, err)
	}
	n, err := strconv.Atoi(header.Get("Content-Length"))
	if err != nil || n < 0 {
		return nil, fmt.Errorf("%w: bad Content-Length", ErrInvalidFrame)
	}
	if n > f.max {
		return nil, ErrFrameTooLarge
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(f.r, body); err != nil {
		return nil, fmt.Errorf("%w: body: %w", ErrInvalidFrame, err)
	}
	return body, nil
}

// FrameWriter serialises frames from concurrent callers.
type FrameWriter struct {
	mu            sync.Mutex
	w             io.Writer
	contentLength bool
}

// NewFrameWriter builds a writer for opts.
func NewFrameWriter(w io.Writer, opts Options) *FrameWriter {
	return &FrameWriter{w: w, contentLength: opts.ContentLength}
}

// WriteFrame writes one payload with the selected framing.
func (f *FrameWriter) WriteFrame(payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	frame := make([]byte, 0, len(payload)+32)
	if f.contentLength {
		frame = append(frame, "Content-Length: "...)
		frame = strconv.AppendInt(frame, int64(len(payload)), 10)
		frame = append(frame, "\r\n\r\n"...)
	}
	frame = append(frame, payload...)
	if !f.contentLength {
		frame = append(frame, '\n')
	}
	if err := writeAll(f.w, frame); err != nil {
		return fmt.Errorf("jsonrpc: write frame: %w", err)
	}
	return nil
}

func writeAll(w io.Writer, p []byte) error {
	n, err := w.Write(p)
	if err != nil {
		return fmt.Errorf("jsonrpc: short write: %w", err)
	}
	if n != len(p) {
		return io.ErrShortWrite
	}
	return nil
}
