package exec

// EC-WIN-008 (EDGE-CASES-hermes.md): the control pipe identifies its peer from
// the kernel's token (SID + logon session), not from a PID the client sends;
// the first bytes the client writes are withheld unless the token check passes.
// The tests below use real named pipes on the Windows CI runner; none of them
// skips.

import (
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

var pipeTestSeq atomic.Uint64

// pipeTestName is unique per test process and case: a stale instance from an
// earlier run would otherwise accept the connection.
func pipeTestName(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf(`damnbot-test-%d-%d`, os.Getpid(), pipeTestSeq.Add(1))
}

type pipeAccept struct {
	conn *PipeConn
	err  error
}

// acceptAsync runs one Accept and always returns a result the test reads, so a
// blocked Accept can never leak past the test (goleak in TestMain).
func acceptAsync(srv *PipeServer) <-chan pipeAccept {
	ch := make(chan pipeAccept, 1)
	go func() {
		c, err := srv.Accept()
		ch <- pipeAccept{c, err}
	}()
	return ch
}

func waitAccept(t *testing.T, ch <-chan pipeAccept) pipeAccept {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("pipe Accept did not return")
		return pipeAccept{}
	}
}

type pipeRead struct {
	msg []byte
	err error
}

// readAsync starts the server read that authenticates the peer, so a client's
// first write always has a reader: on the Windows 2025 runner a write with no
// pending read did not complete even though the pipe has kernel buffers.
func readAsync(conn *PipeConn, n int) <-chan pipeRead {
	ch := make(chan pipeRead, 1)
	go func() {
		buf := make([]byte, n)
		read, err := conn.Read(buf)
		ch <- pipeRead{msg: buf[:read], err: err}
	}()
	return ch
}

func waitRead(t *testing.T, ch <-chan pipeRead) pipeRead {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("pipe read did not return")
		return pipeRead{}
	}
}

// idleTimeout bounds every blocking pipe operation a failing case could leave
// behind.
const idleTimeout = 5 * time.Second

func startPipe(t *testing.T, cfg PipeConfig) *PipeServer {
	t.Helper()
	srv, err := ListenPipe(cfg)
	if err != nil {
		t.Fatalf("ListenPipe: %v", err)
	}
	t.Cleanup(srv.Close)
	return srv
}

// closePipe closes one connection and reports a failure as a test error.
func closePipe(t *testing.T, conn *PipeConn) {
	t.Helper()
	if err := conn.Close(); err != nil {
		t.Errorf("close pipe: %v", err)
	}
}

// cleanupPipe closes the connection when the test ends.
func cleanupPipe(t *testing.T, conn *PipeConn) {
	t.Helper()
	t.Cleanup(func() { closePipe(t, conn) })
}

// TestPipePeerAuth runs the real named-pipe matrix on Windows. The catalog
// gate (EC-WIN-008) lives in the cross-OS TestAuthorizePeer.
func TestPipePeerAuth(t *testing.T) {
	ecWin008(t)
}

func ecWin008(t *testing.T) {
	self, err := currentPipeIdentity()
	if err != nil {
		t.Fatalf("current identity: %v", err)
	}
	t.Run("same-user", func(t *testing.T) { pipeSameUser(t, self) })
	t.Run("wrong-sid", func(t *testing.T) { pipeWrongSID(t, self) })
	t.Run("wrong-session", func(t *testing.T) { pipeWrongSession(t, self) })
	t.Run("foreign-user-dacl", func(t *testing.T) { pipeForeignUser(t) })
}

// pipeSameUser: the client of the server's own user and logon session is
// accepted, its verified identity is reported after the first read, and the
// connection carries bytes both ways.
func pipeSameUser(t *testing.T, self PipeIdentity) {
	name := pipeTestName(t)
	srv := startPipe(t, PipeConfig{Name: name})
	ch := acceptAsync(srv)
	cli, err := DialPipe(name, idleTimeout)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	cleanupPipe(t, cli)
	if err := cli.SetDeadline(time.Now().Add(idleTimeout)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	got := waitAccept(t, ch)
	if got.err != nil {
		t.Fatalf("accept: %v", got.err)
	}
	conn := got.conn
	cleanupPipe(t, conn)
	if err := conn.SetDeadline(time.Now().Add(idleTimeout)); err != nil {
		t.Fatalf("deadline: %v", err)
	}
	// The client speaks first, and the server read is already pending when it
	// writes: Windows only exposes the client token after that read, and a
	// write with no reader must not be relied on to buffer.
	reads := readAsync(conn, 4)
	if _, err := cli.Write([]byte("ping")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	r := waitRead(t, reads)
	if r.err != nil || string(r.msg) != "ping" {
		t.Fatalf("server read = %q, %v", r.msg, r.err)
	}
	peer, ok := conn.Peer()
	if !ok {
		t.Fatal("peer identity was not verified by the first read")
	}
	if peer.SID != self.SID.String() {
		t.Errorf("peer SID = %q, want %q", peer.SID, self.SID.String())
	}
	if peer.Session != self.Session {
		t.Errorf("peer session = %d, want %d", peer.Session, self.Session)
	}
	if peer.User == "" {
		t.Error("peer user is empty")
	}
	if _, err := conn.Write([]byte("pong")); err != nil {
		t.Fatalf("server write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := cli.Read(buf); err != nil || string(buf) != "pong" {
		t.Fatalf("client read = %q, %v", buf, err)
	}
}

// pipeWrongSID: a client that reaches the pipe (the DACL still names its user)
// but whose token user is not the accepted SID is rejected, and the server
// keeps accepting afterwards.
func pipeWrongSID(t *testing.T, self PipeIdentity) {
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatalf("system SID: %v", err)
	}
	name := pipeTestName(t)
	srv := startPipe(t, PipeConfig{Name: name, Want: &PipeIdentity{SID: system, Session: self.Session}})
	pipeRejectedTwice(t, srv, name)
}

// pipeWrongSession: the same user in a different logon session is rejected.
func pipeWrongSession(t *testing.T, self PipeIdentity) {
	name := pipeTestName(t)
	srv := startPipe(t, PipeConfig{Name: name, Want: &PipeIdentity{SID: self.SID, Session: self.Session + 1}})
	pipeRejectedTwice(t, srv, name)
}

// pipeRejectedTwice proves the rejection is the token check (the client could
// open the pipe and its bytes reach the server) and that a rejected peer does
// not kill the server: a second client is rejected the same way instead of
// hanging or being accepted.
func pipeRejectedTwice(t *testing.T, srv *PipeServer, name string) {
	for attempt := 1; attempt <= 2; attempt++ {
		ch := acceptAsync(srv)
		cli, err := DialPipe(name, idleTimeout)
		if err != nil {
			t.Fatalf("attempt %d: dial: %v", attempt, err)
		}
		cleanupPipe(t, cli)
		if err := cli.SetDeadline(time.Now().Add(idleTimeout)); err != nil {
			t.Fatalf("attempt %d: deadline: %v", attempt, err)
		}
		got := waitAccept(t, ch)
		if got.err != nil {
			t.Fatalf("attempt %d: accept: %v", attempt, got.err)
		}
		conn := got.conn
		cleanupPipe(t, conn)
		if err := conn.SetDeadline(time.Now().Add(idleTimeout)); err != nil {
			t.Fatalf("attempt %d: deadline: %v", attempt, err)
		}
		reads := readAsync(conn, 1)
		if _, err := cli.Write([]byte("x")); err != nil {
			t.Fatalf("attempt %d: client write: %v", attempt, err)
		}
		if r := waitRead(t, reads); !errors.Is(r.err, ErrPeerRejected) {
			t.Fatalf("attempt %d: first read = %v, want ErrPeerRejected", attempt, r.err)
		}
		if _, ok := conn.Peer(); ok {
			t.Fatalf("attempt %d: rejected peer reported a verified identity", attempt)
		}
	}
}

// pipeForeignUser: with a SYSTEM-only DACL the kernel itself refuses the
// server's own user, which is the "different user" case without a second
// account or elevation on the runner. No Accept is pending afterwards, so the
// wait is cancelled by Close.
func pipeForeignUser(t *testing.T) {
	name := pipeTestName(t)
	srv := startPipe(t, PipeConfig{Name: name, SDDL: "D:P(A;;GA;;;SY)"})
	ch := acceptAsync(srv)
	_, err := DialPipe(name, idleTimeout)
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("dial error = %v, want ERROR_ACCESS_DENIED", err)
	}
	srv.Close()
	if got := waitAccept(t, ch); !errors.Is(got.err, ErrPipeClosed) {
		t.Fatalf("accept error = %v, want ErrPipeClosed", got.err)
	}
}
