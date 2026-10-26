//go:build unix

package acp

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func closePipe(t *testing.T, f *os.File) {
	t.Helper()
	if err := f.Close(); err != nil {
		t.Logf("close pipe: %v", err)
	}
}

// TestFileWriterDeadline drives a real OS pipe with no reader: the write must
// stop at the deadline instead of blocking forever.
func TestFileWriterDeadline(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer closePipe(t, r)
	defer closePipe(t, w)
	c := NewConn(strings.NewReader(""), FileWriter{F: w})
	c.WriteTimeout = 30 * time.Millisecond
	start := time.Now()
	err = c.WriteResponse(context.Background(), &Response{ID: []byte(`1`), Result: strings.Repeat("x", 1<<20)})
	took := time.Since(start)
	if !errors.Is(err, ErrWriteTimeout) {
		t.Fatalf("err = %v, want ErrWriteTimeout", err)
	}
	if took > time.Second {
		t.Fatalf("deadline took %v", took)
	}
}

// TestFileWriterOK writes a small frame through a drained pipe.
func TestFileWriterOK(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer closePipe(t, w)
	done := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 256)
		n, rerr := r.Read(buf)
		done <- buf[:n]
		if rerr != nil {
			t.Logf("pipe read: %v", rerr)
		}
		closePipe(t, r)
	}()
	c := NewConn(strings.NewReader(""), FileWriter{F: w})
	if err := c.WriteResponse(context.Background(), &Response{ID: []byte(`1`), Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	if got := <-done; !strings.HasSuffix(string(got), "\n") {
		t.Fatalf("frame = %q", got)
	}
}
