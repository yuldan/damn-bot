package exec

// EC-WIN-015 (EDGE-CASES-hermes.md): a shared pipe multiplexer must not
// deadlock or mix messages between clients. These tests use in-memory pipes,
// so they run on every OS (including -race on Linux); mux_windows_test.go
// repeats the client matrix over real named pipes on Windows CI.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// senderAlphabet names the payload of sender s by index, so no test needs an
// int-to-byte conversion.
const senderAlphabet = "abcdefgh"

// pairListener feeds Serve from in-memory net.Pipe connections.
type pairListener struct {
	ch     chan Conn
	closed chan struct{}
	once   sync.Once
}

func newPairListener(capacity int) *pairListener {
	return &pairListener{ch: make(chan Conn, capacity), closed: make(chan struct{})}
}

func (l *pairListener) Accept() (Conn, error) {
	select {
	case c := <-l.ch:
		return c, nil
	case <-l.closed:
		return nil, fmt.Errorf("exec: test listener closed: %w", net.ErrClosed)
	}
}

func (l *pairListener) Close() {
	l.once.Do(func() { close(l.closed) })
}

// dialPair queues the server end and returns the client end.
func dialPair(t *testing.T, l *pairListener) net.Conn {
	t.Helper()
	srv, cli := net.Pipe()
	select {
	case l.ch <- srv:
		return cli
	case <-time.After(5 * time.Second):
		t.Fatal("listener did not accept")
		return nil
	}
}

// serveTestMux runs Serve in the background; the returned cancel stops it.
func serveTestMux(mux *Mux, ln Listener, handler func(*Client)) (context.CancelFunc, <-chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- mux.Serve(ctx, ln, handler) }()
	return cancel, served
}

func echoHandler(ctx context.Context) func(*Client) {
	return func(c *Client) {
		for {
			msg, err := c.Receive()
			if err != nil {
				return
			}
			if err := c.Send(ctx, msg); err != nil {
				return
			}
		}
	}
}

func deadline(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := conn.SetDeadline(time.Now().Add(60 * time.Second)); err != nil {
		t.Errorf("set deadline: %v", err)
	}
}

func closeConn(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := conn.Close(); err != nil {
		t.Errorf("close connection: %v", err)
	}
}

// waitServe stops the mux and asserts it returned without an error.
func waitServe(t *testing.T, cancel context.CancelFunc, served <-chan error) {
	t.Helper()
	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not stop after cancel")
	}
}

// TestMuxFrameIsolation is the cross-OS catalog entry for EC-WIN-015.
func TestMuxFrameIsolation(t *testing.T) {
	g := phase.Current(t)
	if g.Item(t, "EC-WIN-015", "F5", "covered") {
		t.Run("EC-WIN-015", ecWin015)
	}
}

// ecWin015: ten parallel clients exchange one hundred messages each; every
// client receives exactly its own messages, in its own order.
func ecWin015(t *testing.T) {
	const (
		clients  = 10
		messages = 100
	)
	ln := newPairListener(clients)
	cancel, served := serveTestMux(&Mux{}, ln, echoHandler(context.Background()))
	defer cancel()
	conns := make([]net.Conn, clients)
	for i := range conns {
		conns[i] = dialPair(t, ln)
	}
	var wg sync.WaitGroup
	for id := range conns {
		wg.Go(func() { echoClient(t, conns[id], id, messages) })
	}
	wg.Wait()
	waitServe(t, cancel, served)
}

// echoClient sends its numbered frames and checks the echo belongs to it.
func echoClient(t *testing.T, conn net.Conn, id, messages int) {
	t.Helper()
	defer closeConn(t, conn)
	deadline(t, conn)
	fr := Framer{}
	writes := make(chan error, 1)
	go func() { writes <- writeFrames(fr, conn, id, messages) }()
	for seq := range messages {
		msg, err := fr.Read(conn)
		if err != nil {
			t.Errorf("client %d frame %d: %v", id, seq, err)
			return
		}
		if want := fmt.Sprintf("%d:%d", id, seq); string(msg) != want {
			t.Errorf("client %d received %q, want %q", id, msg, want)
			return
		}
	}
	if err := <-writes; err != nil {
		t.Errorf("client %d write: %v", id, err)
	}
}

func writeFrames(fr Framer, conn net.Conn, id, messages int) error {
	for seq := range messages {
		if err := fr.Write(conn, fmt.Appendf(nil, "%d:%d", id, seq)); err != nil {
			return err
		}
	}
	return nil
}

// TestMuxSerializedSends: concurrent Send calls on one client never interleave
// on the wire; every frame arrives whole.
func TestMuxSerializedSends(t *testing.T) {
	const (
		senders   = 8
		perSender = 50
		width     = 200
	)
	ln := newPairListener(1)
	release := make(chan struct{})
	cancel, served := serveTestMux(&Mux{}, ln, func(c *Client) {
		sendBurst(t, c, senders, perSender, width)
		<-release // returning here would drop frames the peer has not read yet
	})
	defer cancel()
	conn := dialPair(t, ln)
	defer closeConn(t, conn)
	deadline(t, conn)
	validateBurst(t, conn, senders, perSender, width)
	close(release)
	waitServe(t, cancel, served)
}

// sendBurst has one goroutine per sender push frames through the same client.
func sendBurst(t *testing.T, c *Client, senders, perSender, width int) {
	t.Helper()
	var wg sync.WaitGroup
	for s := range senders {
		wg.Go(func() {
			payload := bytes.Repeat([]byte{senderAlphabet[s]}, width)
			for range perSender {
				if err := c.Send(context.Background(), payload); err != nil {
					t.Errorf("sender %d: %v", s, err)
					return
				}
			}
		})
	}
	wg.Wait()
}

// validateBurst checks every received frame is one whole sender payload and
// each sender delivered exactly perSender frames.
func validateBurst(t *testing.T, conn net.Conn, senders, perSender, width int) {
	t.Helper()
	fr := Framer{}
	counts := make([]int, senders)
	for i := range senders * perSender {
		msg, err := fr.Read(conn)
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if len(msg) != width {
			t.Fatalf("frame %d has %d bytes, want %d (writes interleaved)", i, len(msg), width)
		}
		for _, b := range msg {
			if b != msg[0] {
				t.Fatalf("frame %d corrupted: %q", i, msg)
			}
		}
		sender := strings.IndexByte(senderAlphabet, msg[0])
		if sender < 0 || sender >= senders {
			t.Fatalf("frame %d has unknown sender %q", i, msg[0])
		}
		counts[sender]++
	}
	for s := range senders {
		if counts[s] != perSender {
			t.Errorf("sender %d delivered %d frames, want %d", s, counts[s], perSender)
		}
	}
}

// TestMuxBackpressure: a peer that stops reading stalls the write under the
// connection mutex, so the next Send waits instead of buffering without bound;
// the queue depth of the server stays one frame.
func TestMuxBackpressure(t *testing.T) {
	ln := newPairListener(1)
	started := make(chan struct{}, 2)
	done := make(chan struct{})
	cancel, served := serveTestMux(&Mux{}, ln, func(c *Client) {
		if _, err := c.Receive(); err != nil {
			close(done)
			return
		}
		var wg sync.WaitGroup
		for range 2 {
			wg.Go(func() {
				started <- struct{}{}
				if err := c.Send(context.Background(), []byte("frame")); err != nil {
					t.Errorf("send: %v", err)
				}
			})
		}
		wg.Wait()
		close(done)
	})
	defer cancel()
	conn := dialPair(t, ln)
	if err := (Framer{}).Write(conn, []byte("hello")); err != nil {
		t.Fatalf("hello: %v", err)
	}
	<-started
	<-started
	select {
	case <-done:
		t.Fatal("sends finished while the peer was not reading")
	case <-time.After(150 * time.Millisecond):
	}
	deadline(t, conn)
	fr := Framer{}
	for range 2 {
		if _, err := fr.Read(conn); err != nil {
			t.Fatalf("read blocked frame: %v", err)
		}
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("sends did not finish after the peer read")
	}
	closeConn(t, conn)
	waitServe(t, cancel, served)
}

// TestMuxClientDeath: a client that dies mid-stream ends only its own handler;
// the server keeps accepting and serving other clients.
func TestMuxClientDeath(t *testing.T) {
	ln := newPairListener(4)
	cancel, served := serveTestMux(&Mux{}, ln, echoHandler(context.Background()))
	defer cancel()
	dead := dialPair(t, ln)
	if err := (Framer{}).Write(dead, []byte("bye")); err != nil {
		t.Fatalf("dead client write: %v", err)
	}
	closeConn(t, dead)
	exchange := func(id string) {
		t.Helper()
		conn := dialPair(t, ln)
		defer closeConn(t, conn)
		deadline(t, conn)
		fr := Framer{}
		for i := range 20 {
			msg := fmt.Appendf(nil, "%s:%d", id, i)
			if err := fr.Write(conn, msg); err != nil {
				t.Fatalf("%s write %d: %v", id, i, err)
			}
			got, err := fr.Read(conn)
			if err != nil {
				t.Fatalf("%s read %d: %v", id, i, err)
			}
			if !bytes.Equal(got, msg) {
				t.Fatalf("%s received %q, want %q", id, got, msg)
			}
		}
	}
	exchange("live")
	select {
	case err := <-served:
		t.Fatalf("Serve stopped after a client died: %v", err)
	default:
	}
	exchange("fresh")
	waitServe(t, cancel, served)
}

// TestFramerLimits locks the wire format and its refusals without any pipe.
func TestFramerLimits(t *testing.T) {
	t.Parallel()
	fr := Framer{MaxFrame: 8}
	var buf bytes.Buffer
	if err := fr.Write(&buf, []byte("12345678")); err != nil {
		t.Fatalf("write max: %v", err)
	}
	if got, err := fr.Read(&buf); err != nil || string(got) != "12345678" {
		t.Fatalf("read max = %q, %v", got, err)
	}
	if err := fr.Write(&buf, []byte("123456789")); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversize write = %v, want ErrFrameTooLarge", err)
	}
	if err := fr.Write(&buf, nil); !errors.Is(err, ErrEmptyFrame) {
		t.Fatalf("empty write = %v, want ErrEmptyFrame", err)
	}
	if _, err := fr.Read(bytes.NewReader([]byte{0, 0, 0, 0})); !errors.Is(err, ErrEmptyFrame) {
		t.Fatalf("empty read = %v, want ErrEmptyFrame", err)
	}
	if _, err := fr.Read(bytes.NewReader([]byte{0, 0, 0, 9})); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversize read = %v, want ErrFrameTooLarge", err)
	}
}
