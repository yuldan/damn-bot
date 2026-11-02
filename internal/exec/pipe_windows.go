package exec

// Windows control pipe (EC-WIN-008): every accepted peer is identified from
// the token the kernel attaches to the pipe handle, never from a PID or user
// name the client reports. The pipe itself is created with an owner+SYSTEM
// DACL, so the kernel refuses other accounts before a byte is exchanged; the
// server then reads the client's first bytes, impersonates the client, checks
// its token (user SID, logon session, impersonation level), and withholds
// those bytes unless the token matches the identity configured for the server.
// Tokens never leave this file: PipePeer carries only the public identity
// (user name, SID string, session id) and no handle or raw token is logged.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	pipePrefix = `\\.\pipe\`
	// pipeRetryInterval paces CreateFile retries while every instance is busy
	// or the first instance has not been created yet.
	pipeRetryInterval = 5 * time.Millisecond
	// pipeConnectPoll is the wait quantum of a pending ConnectNamedPipe, so a
	// closed server unblocks a waiting Accept without another client.
	pipeConnectPoll = 50 * time.Millisecond
	// pipeBuffer is the per-direction kernel buffer. The default (0) made a
	// client write wait for a server read on the Windows 2025 runner even
	// though the client speaks first and its first bytes are tiny.
	pipeBuffer = 64 << 10
)

// ErrPipeClosed reports a server that was closed while accepting.
var ErrPipeClosed = errors.New("exec: pipe server closed")

// PipePeer is the verified identity of a client. It never carries the access
// token, a token handle, or a PID supplied by the client.
type PipePeer struct {
	User    string // DOMAIN\user, or the SID when the account cannot be resolved
	SID     string // S-1-5-... string form of the token user
	Session uint32 // logon session id of the client token
}

// PipeIdentity is the client identity a server accepts. SID is required;
// Session is the exact logon session (0 is a session like any other, so an
// unset session is expressed by a nil Want).
type PipeIdentity struct {
	SID     *windows.SID
	Session uint32
}

// PipeConfig configures ListenPipe. Want nil accepts the identity of the
// process that created the server (current user and logon session), which is
// the local control-plane case.
type PipeConfig struct {
	Name string // short name, or a full `\\.\pipe\...` path
	SDDL string // empty = DefaultPipeSDDL
	Want *PipeIdentity
}

// PipeServer is a Windows named-pipe listener that authenticates each peer
// before handing the connection to the caller. One instance is pending at a
// time: clients that connect while it is busy retry until an instance exists.
type PipeServer struct {
	path string
	sd   *windows.SECURITY_DESCRIPTOR
	want PipeIdentity
	quit atomic.Bool
}

// ListenPipe creates the pipe name; clients can connect as soon as Accept is
// pending. The DACL is applied to every instance, including instances created
// for later clients.
func ListenPipe(cfg PipeConfig) (*PipeServer, error) {
	if cfg.Name == "" {
		return nil, errors.New("exec: pipe name is required")
	}
	path := cfg.Name
	if !strings.HasPrefix(strings.ToLower(path), pipePrefix) {
		path = pipePrefix + path
	}
	want := PipeIdentity{}
	if cfg.Want != nil {
		want = *cfg.Want
	}
	if want.SID == nil {
		var err error
		if want, err = currentPipeIdentity(); err != nil {
			return nil, err
		}
	}
	sd, err := windows.SecurityDescriptorFromString(pipeSDDL(cfg.SDDL))
	if err != nil {
		return nil, fmt.Errorf("exec: pipe security descriptor: %w", err)
	}
	return &PipeServer{path: path, sd: sd, want: want}, nil
}

// pipeSDDL returns the DACL of every instance: full control to SYSTEM and to
// the object owner (the user that created the pipe), nothing to anyone else.
// An explicit client SDDL replaces it, which the tests use to prove the kernel
// refuses a user the DACL does not name.
func pipeSDDL(explicit string) string {
	if explicit != "" {
		return explicit
	}
	return "D:P(A;;GA;;;SY)(A;;GA;;;OW)"
}

// Close stops Accept. A pending Accept returns ErrPipeClosed; accepted
// connections are owned by the caller and are not closed here. Closing never
// fails: a pending Accept reports the outcome.
func (s *PipeServer) Close() {
	s.quit.Store(true)
}

// Accept waits for one client and returns the connection. The identity is
// verified on the first Read, because Windows only exposes the client token
// after the server has read from the pipe; a connection whose peer fails the
// check returns ErrPeerRejected from that Read and the server stays usable.
func (s *PipeServer) Accept() (*PipeConn, error) {
	if s.quit.Load() {
		return nil, ErrPipeClosed
	}
	h, err := s.instance()
	if err != nil {
		return nil, err
	}
	if s.quit.Load() {
		_ = windows.CloseHandle(h)
		return nil, ErrPipeClosed
	}
	if err := s.connect(h); err != nil {
		_ = windows.CloseHandle(h)
		return nil, err
	}
	return &PipeConn{handle: h, server: true, want: s.want}, nil
}

// instance creates one pipe instance with the configured DACL, in overlapped
// mode so the pending ConnectNamedPipe can be abandoned on Close.
func (s *PipeServer) instance() (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(s.path)
	if err != nil {
		return 0, fmt.Errorf("exec: pipe name: %w", err)
	}
	sa := windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: s.sd,
	}
	h, err := windows.CreateNamedPipe(name,
		windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		windows.PIPE_UNLIMITED_INSTANCES, pipeBuffer, pipeBuffer, 0, &sa)
	if err != nil {
		return 0, fmt.Errorf("exec: create pipe: %w", err)
	}
	return h, nil
}

// connect waits for a client on one instance without ever blocking Close: the
// overlapped wait is polled so the quit flag is seen within pipeConnectPoll.
func (s *PipeServer) connect(h windows.Handle) error {
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return fmt.Errorf("exec: pipe event: %w", err)
	}
	defer func() { _ = windows.CloseHandle(ev) }()
	ov := &windows.Overlapped{HEvent: ev}
	switch err := windows.ConnectNamedPipe(h, ov); {
	case err == nil, errors.Is(err, windows.ERROR_PIPE_CONNECTED):
		return nil
	case !errors.Is(err, windows.ERROR_IO_PENDING):
		return fmt.Errorf("exec: connect pipe: %w", err)
	}
	for {
		evt, err := windows.WaitForSingleObject(ev, uint32(pipeConnectPoll/time.Millisecond))
		if err != nil {
			return fmt.Errorf("exec: wait pipe: %w", err)
		}
		if evt == windows.WAIT_OBJECT_0 {
			var done uint32
			if err := windows.GetOverlappedResult(h, ov, &done, false); err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
				return fmt.Errorf("exec: connect pipe: %w", err)
			}
			return nil
		}
		if s.quit.Load() {
			_ = windows.CancelIoEx(h, ov)
			var done uint32
			_ = windows.GetOverlappedResult(h, ov, &done, true)
			return ErrPipeClosed
		}
	}
}

// PipeConn is a pipe connection. On the server side the peer is verified on
// the first Read (Windows exposes the client token only after the server has
// read data), and no byte is returned before that check passes. Read and Write
// are plain stream operations; the multiplexer owns any framing.
type PipeConn struct {
	handle windows.Handle
	server bool
	want   PipeIdentity

	dlMu     sync.Mutex
	deadline time.Time

	opMu   sync.Mutex
	closed bool
	ops    sync.WaitGroup

	authOnce sync.Once
	peer     PipePeer
	authErr  error
	authed   bool
}

// Read reads from the pipe. On a server connection the first delivered bytes
// authenticate the peer and are withheld unless the token check passes; a
// rejection returns ErrPeerRejected.
func (c *PipeConn) Read(p []byte) (int, error) {
	n, err := c.io(p, true)
	if !c.server || n == 0 {
		return n, err
	}
	if aerr := c.authenticate(); aerr != nil {
		return 0, aerr
	}
	return n, err
}

// Write writes to the pipe. A server connection must have authenticated its
// peer first: the client speaks first, so a Write before the first Read is
// reported as rejected instead of reaching an unverified peer.
func (c *PipeConn) Write(p []byte) (int, error) {
	if c.server {
		if aerr := c.authenticate(); aerr != nil {
			return 0, aerr
		}
	}
	return c.io(p, false)
}

// io runs one overlapped ReadFile/WriteFile with its own event, so a Read and
// a Write may be in flight at the same time. os.File cannot host that: Go's
// poll keeps file-offset state per File and a concurrent Read and Write on one
// File is a data race (the Windows -race job caught it in addOffset).
func (c *PipeConn) io(p []byte, read bool) (int, error) {
	if !c.begin() {
		return 0, errors.New("exec: pipe connection closed")
	}
	defer c.ops.Done()
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return 0, fmt.Errorf("exec: pipe event: %w", err)
	}
	defer func() { _ = windows.CloseHandle(ev) }()
	ov := &windows.Overlapped{HEvent: ev}
	var n uint32
	if read {
		err = windows.ReadFile(c.handle, p, &n, ov)
	} else {
		err = windows.WriteFile(c.handle, p, &n, ov)
	}
	switch {
	case err == nil:
		return int(n), nil
	case !errors.Is(err, windows.ERROR_IO_PENDING):
		return 0, fmt.Errorf("exec: pipe io: %w", err)
	}
	evt, err := windows.WaitForSingleObject(ev, c.timeoutMillis())
	if err != nil {
		return 0, fmt.Errorf("exec: wait pipe io: %w", err)
	}
	if evt != windows.WAIT_OBJECT_0 {
		_ = windows.CancelIoEx(c.handle, ov)
		var done uint32
		_ = windows.GetOverlappedResult(c.handle, ov, &done, true)
		return 0, os.ErrDeadlineExceeded
	}
	if err := windows.GetOverlappedResult(c.handle, ov, &n, false); err != nil {
		return 0, fmt.Errorf("exec: pipe io: %w", err)
	}
	return int(n), nil
}

func (c *PipeConn) begin() bool {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if c.closed {
		return false
	}
	c.ops.Add(1)
	return true
}

func (c *PipeConn) timeoutMillis() uint32 {
	c.dlMu.Lock()
	defer c.dlMu.Unlock()
	if c.deadline.IsZero() {
		return windows.INFINITE
	}
	left := time.Until(c.deadline).Milliseconds()
	switch {
	case left <= 0:
		return 0
	case left > math.MaxUint32:
		return math.MaxUint32
	default:
		return uint32(left)
	}
}

// authenticate verifies the peer once. It runs after the first read, which is
// what makes the client token available to ImpersonateNamedPipeClient.
func (c *PipeConn) authenticate() error {
	c.authOnce.Do(func() {
		c.peer, c.authErr = authenticatePipePeer(c.handle, c.want)
		c.authed = c.authErr == nil
		if c.authErr != nil {
			_ = windows.DisconnectNamedPipe(c.handle)
		}
	})
	return c.authErr
}

// Peer reports the verified identity and whether it was established. It is
// only meaningful after a successful Read on a server connection.
func (c *PipeConn) Peer() (PipePeer, bool) { return c.peer, c.authed }

// Close disconnects the server side (the peer sees a broken pipe), cancels any
// in-flight overlapped I/O, and closes the handle once no operation is using it.
func (c *PipeConn) Close() error {
	var derr error
	if c.server {
		if derr = windows.DisconnectNamedPipe(c.handle); errors.Is(derr, windows.ERROR_PIPE_NOT_CONNECTED) {
			derr = nil
		}
	}
	c.opMu.Lock()
	c.closed = true
	c.opMu.Unlock()
	_ = windows.CancelIoEx(c.handle, nil)
	c.ops.Wait()
	cerr := windows.CloseHandle(c.handle)
	if cerr != nil && errors.Is(cerr, windows.ERROR_INVALID_HANDLE) {
		cerr = nil // already closed by DisconnectNamedPipe
	}
	return errors.Join(derr, cerr)
}

// SetDeadline bounds the wait of subsequent Read and Write operations; a zero
// time clears it.
func (c *PipeConn) SetDeadline(t time.Time) error {
	c.dlMu.Lock()
	c.deadline = t
	c.dlMu.Unlock()
	return nil
}

// DialPipe connects to a server pipe, retrying while every instance is busy
// or the server has not created its first instance yet.
func DialPipe(name string, timeout time.Duration) (*PipeConn, error) {
	path := name
	if !strings.HasPrefix(strings.ToLower(path), pipePrefix) {
		path = pipePrefix + path
	}
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("exec: pipe name: %w", err)
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		h, err := windows.CreateFile(ptr, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
			windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
		if err == nil {
			return &PipeConn{handle: h}, nil
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil, fmt.Errorf("exec: dial pipe: %w", err)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("exec: dial pipe: %w", err)
		}
		time.Sleep(pipeRetryInterval)
	}
}

// currentPipeIdentity is the identity ListenPipe accepts by default.
func currentPipeIdentity() (PipeIdentity, error) {
	tok := windows.GetCurrentProcessToken()
	u, err := tok.GetTokenUser()
	if err != nil {
		return PipeIdentity{}, fmt.Errorf("exec: current token user: %w", err)
	}
	sess, err := tokenDWORD(tok, windows.TokenSessionId)
	if err != nil {
		return PipeIdentity{}, fmt.Errorf("exec: current token session: %w", err)
	}
	return PipeIdentity{SID: u.User.Sid, Session: sess}, nil
}

// impersonateNamedPipeClient attaches the client's token to the calling OS
// thread (advapi32, absent from x/sys v0.49). The caller must hold the thread
// and revert with RevertToSelf before releasing it.
func impersonateNamedPipeClient(h windows.Handle) error {
	r1, _, err := windows.NewLazySystemDLL("advapi32.dll").NewProc("ImpersonateNamedPipeClient").Call(uintptr(h))
	if r1 != 0 {
		return nil
	}
	if err == nil || errors.Is(err, windows.ERROR_SUCCESS) {
		return errors.New("ImpersonateNamedPipeClient failed")
	}
	return err
}

// authenticatePipePeer impersonates the client on the accepting OS thread and
// verifies the token it presents. The impersonation is always reverted before
// the thread is released; nothing derived from the token is logged.
func authenticatePipePeer(h windows.Handle, want PipeIdentity) (PipePeer, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := impersonateNamedPipeClient(h); err != nil {
		return PipePeer{}, fmt.Errorf("%w: impersonate: %v", ErrPeerRejected, err)
	}
	defer func() { _ = windows.RevertToSelf() }()
	thread, err := windows.GetCurrentThread()
	if err != nil {
		return PipePeer{}, fmt.Errorf("%w: client thread: %v", ErrPeerRejected, err)
	}
	var tok windows.Token
	if err := windows.OpenThreadToken(thread, windows.TOKEN_QUERY, false, &tok); err != nil {
		return PipePeer{}, fmt.Errorf("%w: client token: %v", ErrPeerRejected, err)
	}
	defer func() { _ = tok.Close() }()
	level, err := tokenDWORD(tok, windows.TokenImpersonationLevel)
	if err != nil {
		return PipePeer{}, fmt.Errorf("%w: token level: %v", ErrPeerRejected, err)
	}
	if level < windows.SecurityImpersonation {
		return PipePeer{}, fmt.Errorf("%w: impersonation level %d", ErrPeerRejected, level)
	}
	peerSID, err := peerSIDFromToken(tok)
	if err != nil {
		return PipePeer{}, err
	}
	if err := authorizePeer(peerSID, want.SID.String()); err != nil {
		return PipePeer{}, err
	}
	sess, err := tokenDWORD(tok, windows.TokenSessionId)
	if err != nil {
		return PipePeer{}, fmt.Errorf("%w: token session: %v", ErrPeerRejected, err)
	}
	if sess != want.Session {
		return PipePeer{}, fmt.Errorf("%w: session %d", ErrPeerRejected, sess)
	}
	u, err := tok.GetTokenUser()
	if err != nil {
		return PipePeer{}, fmt.Errorf("%w: token user: %v", ErrPeerRejected, err)
	}
	return PipePeer{User: pipeUserName(u.User.Sid), SID: peerSID, Session: sess}, nil
}

// peerSIDFromToken is the Windows side of the authorization input: the user
// SID string carried by the impersonated client token.
func peerSIDFromToken(tok windows.Token) (string, error) {
	u, err := tok.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("%w: token user: %v", ErrPeerRejected, err)
	}
	if u.User.Sid == nil {
		return "", fmt.Errorf("%w: token without a user SID", ErrPeerRejected)
	}
	return u.User.Sid.String(), nil
}

// pipeUserName is the display form of a peer SID; unresolved accounts (domain
// unreachable, builtin service accounts) fall back to the SID string.
func pipeUserName(sid *windows.SID) string {
	account, domain, _, err := sid.LookupAccount("")
	switch {
	case err != nil:
		return sid.String()
	case domain == "":
		return account
	default:
		return domain + `\` + account
	}
}

// tokenDWORD reads one DWORD token class (session id, impersonation level)
// without unsafe pointer arithmetic.
func tokenDWORD(tok windows.Token, class uint32) (uint32, error) {
	var n uint32
	if err := windows.GetTokenInformation(tok, class, nil, 0, &n); err != nil && !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		return 0, err
	}
	if n < 4 {
		return 0, fmt.Errorf("token class %d returned %d bytes", class, n)
	}
	buf := make([]byte, n)
	if err := windows.GetTokenInformation(tok, class, &buf[0], n, &n); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(buf[:4]), nil
}
