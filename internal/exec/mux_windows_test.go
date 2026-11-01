package exec

// EC-WIN-015 on real named pipes: one control pipe serves ten parallel clients,
// every client receives only its own frames, and a client killed mid-stream
// leaves the server and its siblings running. The catalog gate lives in the
// cross-OS TestMuxFrameIsolation; this is the Windows transport proof and it
// does not skip.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

const pipeMuxDeadline = 90 * time.Second

func TestPipeMuxWindows(t *testing.T) {
	const (
		clients  = 10
		messages = 100
	)
	name := pipeTestName(t)
	srv := startPipe(t, PipeConfig{Name: name})
	mux := &Mux{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- mux.Serve(ctx, srv.Listener(), echoHandler(ctx)) }()
	var wg sync.WaitGroup
	for id := range clients {
		wg.Go(func() {
			if err := pipeEchoClient(t, name, id, messages); err != nil {
				t.Errorf("client %d: %v", id, err)
			}
		})
	}
	wg.Wait()
	if err := pipeDyingClient(name); err != nil {
		t.Fatalf("dying client: %v", err)
	}
	select {
	case err := <-served:
		t.Fatalf("Serve stopped after a client died: %v", err)
	default:
	}
	if err := pipeEchoClient(t, name, clients, 5); err != nil {
		t.Fatalf("client after the death: %v", err)
	}
	waitServe(t, cancel, served)
}

// pipeEchoClient sends its own numbered frames and checks the echo carries
// only that client's frames, in order.
func pipeEchoClient(t *testing.T, name string, id, messages int) error {
	t.Helper()
	conn, err := DialPipe(name, 10*time.Second)
	if err != nil {
		return err
	}
	defer closePipe(t, conn)
	if err := conn.SetDeadline(time.Now().Add(pipeMuxDeadline)); err != nil {
		return err
	}
	fr := Framer{}
	writes := make(chan error, 1)
	go func() {
		for seq := range messages {
			if err := fr.Write(conn, fmt.Appendf(nil, "%d:%d", id, seq)); err != nil {
				writes <- err
				return
			}
		}
		writes <- nil
	}()
	for seq := range messages {
		msg, err := fr.Read(conn)
		if err != nil {
			return err
		}
		if want := fmt.Sprintf("%d:%d", id, seq); string(msg) != want {
			return fmt.Errorf("received %q, want %q", msg, want)
		}
	}
	return <-writes
}

// pipeDyingClient writes a few frames and closes without reading the echo.
func pipeDyingClient(name string) error {
	conn, err := DialPipe(name, 10*time.Second)
	if err != nil {
		return err
	}
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return errors.Join(err, conn.Close())
	}
	fr := Framer{}
	for seq := range 5 {
		if err := fr.Write(conn, fmt.Appendf(nil, "dying:%d", seq)); err != nil {
			return errors.Join(err, conn.Close())
		}
	}
	return conn.Close()
}
