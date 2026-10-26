package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// repeatReader emits n copies of b without allocating n bytes.
type repeatReader struct {
	n int64
	b byte
}

func (r *repeatReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, io.EOF
	}
	n := min(int64(len(p)), r.n)
	for i := range n {
		p[i] = r.b
	}
	r.n -= n
	return int(n), nil
}

func TestFrameSplitOnLFOnly(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-FIO-018", "F3", "covered") {
		return
	}
	t.Run("EC-FIO-018", func(t *testing.T) {
		t.Parallel()
		frame := `{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"text":"a` + "\u2028" + `b` + "\u2029" + `c"}}`
		input := frame + "\n" + `{"jsonrpc":"2.0","id":2,"method":"next"}` + "\n"
		r := NewReader(strings.NewReader(input))
		first, ferr := r.ReadFrame()
		if ferr != nil {
			t.Fatalf("first frame: %v", ferr)
		}
		req, rpcErr := ParseRequest(first)
		if rpcErr != nil {
			t.Fatalf("parse: %v", rpcErr)
		}
		var params struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			t.Fatal(err)
		}
		if params.Text != "a\u2028b\u2029c" {
			t.Fatalf("text = %q", params.Text)
		}
		second, serr := r.ReadFrame()
		if serr != nil {
			t.Fatalf("second frame: %v", serr)
		}
		if req2, e := ParseRequest(second); e != nil || req2.Method != "next" {
			t.Fatalf("second = %+v, %v", req2, e)
		}

		// Negative control: a splitter that also breaks on U+2028/U+2029
		// would turn one frame into three. The framing above must not.
		if got := naiveSplit(frame); len(got) != 3 {
			t.Fatalf("bad splitter produced %d parts, want 3 (documents the failure mode)", len(got))
		}
	})
}

// naiveSplit is the deliberately wrong splitter used only as a negative
// control for EC-FIO-018.
func naiveSplit(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\u2028' || r == '\u2029' })
}

func TestFrameCapBoundary(t *testing.T) {
	t.Parallel()
	r := NewReaderLimit(strings.NewReader("abc\nabcd\n"), 3)
	first, err := r.ReadFrame()
	if err != nil || string(first) != "abc" {
		t.Fatalf("first = %q, %v", first, err)
	}
	if _, err := r.ReadFrame(); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("over-cap = %v, want ErrFrameTooLarge", err)
	}
	if _, err := r.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Fatalf("after drain = %v, want EOF", err)
	}
}

func TestFrameTrailingPartialAccepted(t *testing.T) {
	t.Parallel()
	r := NewReader(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"x"}`))
	frame, err := r.ReadFrame()
	if err != nil || len(frame) == 0 {
		t.Fatalf("partial = %q, %v", frame, err)
	}
	if _, err := r.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Fatalf("next = %v, want EOF", err)
	}
}

func TestConnSkipsBlankLines(t *testing.T) {
	t.Parallel()
	c := NewConn(strings.NewReader("\n \t\r\n"+`{"jsonrpc":"2.0","id":1,"method":"ok"}`+"\n"), discardWriter{})
	req, rpcErr, err := c.Next(context.Background())
	if err != nil || rpcErr != nil {
		t.Fatalf("Next = %v, %v", rpcErr, err)
	}
	if req.Method != "ok" {
		t.Fatalf("method = %q", req.Method)
	}
}

type discardWriter struct{}

func (discardWriter) WriteFrame(context.Context, []byte) error { return nil }

// TestFrame100MBBounded is the real 100 MB line: the reader must return
// -32600, keep memory bounded, resynchronise on the next LF, and leave the
// stream usable.
func TestFrame100MBBounded(t *testing.T) {
	const size = 100 << 20
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	rssBefore := rssBytes()
	start := time.Now()
	input := io.MultiReader(
		&repeatReader{n: size - 1, b: 'a'},
		strings.NewReader("\n"+`{"jsonrpc":"2.0","id":2,"method":"after"}`+"\n"),
	)
	c := NewConn(input, discardWriter{})
	_, rpcErr, err := c.Next(context.Background())
	took := time.Since(start)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if rpcErr == nil || rpcErr.Code != CodeInvalidRequest {
		t.Fatalf("oversize error = %+v, want -32600", rpcErr)
	}
	if strings.Contains(rpcErr.Message, "aaaa") {
		t.Fatalf("error echoes frame content: %q", rpcErr.Message)
	}
	req, rpcErr, err := c.Next(context.Background())
	if err != nil || rpcErr != nil || req.Method != "after" {
		t.Fatalf("resync = %+v, %v, %v", req, rpcErr, err)
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	var heapDelta uint64
	if after.HeapAlloc > before.HeapAlloc {
		heapDelta = after.HeapAlloc - before.HeapAlloc
	}
	rssDelta := rssBytes() - rssBefore
	t.Logf("100 MB frame: took=%v heap_delta=%d KiB rss_delta=%d KiB", took, heapDelta/1024, rssDelta/1024)
	if took > 10*time.Second {
		t.Fatalf("100 MB frame took %v", took)
	}
	if heapDelta > 48<<20 {
		t.Fatalf("heap grew by %d bytes for a capped frame", heapDelta)
	}
	if rssDelta > 256<<20 {
		t.Fatalf("RSS grew by %d bytes for a capped frame", rssDelta)
	}
}

func rssBytes() int64 {
	// Portable fallback: 0 when /proc is unavailable (non-Linux); the test
	// still asserts the heap bound.
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) < 2 {
		return 0
	}
	pages := int64(0)
	for _, c := range fields[1] {
		if c < '0' || c > '9' {
			return 0
		}
		pages = pages*10 + int64(c-'0')
	}
	return pages * int64(4096)
}

type blockingWriter struct{}

func (blockingWriter) WriteFrame(ctx context.Context, _ []byte) error {
	<-ctx.Done()
	return fmt.Errorf("blocked: %w", ctx.Err())
}

func TestWriteDeadline(t *testing.T) {
	t.Parallel()
	c := NewConn(strings.NewReader(""), blockingWriter{})
	c.WriteTimeout = 20 * time.Millisecond
	start := time.Now()
	err := c.WriteResponse(context.Background(), &Response{ID: json.RawMessage(`1`), Result: "ok"})
	took := time.Since(start)
	if !errors.Is(err, ErrWriteTimeout) {
		t.Fatalf("err = %v, want ErrWriteTimeout", err)
	}
	if took < 10*time.Millisecond || took > time.Second {
		t.Fatalf("deadline took %v", took)
	}
}
