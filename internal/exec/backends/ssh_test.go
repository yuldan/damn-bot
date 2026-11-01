//go:build !minimal && unix

package backends

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/netx"
)

// sshTestServer is an in-process SSH server: exec requests run through the
// local sh, so the client's quoting, env, cwd and pid-file cleanup are
// exercised end to end without a network dependency.
type sshTestServer struct {
	addr    string
	accepts atomic.Int32
	done    chan struct{}
	key     ssh.PublicKey
}

func startSSHServer(t *testing.T) *sshTestServer {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(hostPriv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) { return nil, nil },
	}
	cfg.AddHostKey(signer)
	lc := net.ListenConfig{}
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &sshTestServer{addr: ln.Addr().String(), done: make(chan struct{}), key: signer.PublicKey()}
	t.Cleanup(func() {
		//nolint:errcheck,gosec // test listener cleanup
		ln.Close()
		close(s.done)
	})
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			s.accepts.Add(1)
			go s.serve(nc, cfg)
		}
	}()
	return s
}

//nolint:gocognit // a test SSH server is a request loop by nature
func (s *sshTestServer) serve(nc net.Conn, cfg *ssh.ServerConfig) {
	//nolint:errcheck // test connection cleanup
	defer nc.Close()
	_, chans, reqs, err := ssh.NewServerConn(nc, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		if ch.ChannelType() != "session" {
			//nolint:errcheck,gosec // the peer closed the channel
			ch.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		channel, requests, err := ch.Accept()
		if err != nil {
			continue
		}
		go func() {
			//nolint:errcheck // test channel cleanup
			defer channel.Close()
			for req := range requests {
				if req.Type != "exec" {
					//nolint:errcheck,gosec // the session is being torn down
					req.Reply(false, nil)
					continue
				}
				var payload struct{ Command string }
				if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
					//nolint:errcheck,gosec // the session is being torn down
					req.Reply(false, nil)
					continue
				}
				//nolint:errcheck,gosec // the session is being torn down
				req.Reply(true, nil)
				//nolint:gosec // the test server deliberately executes the client's command
				cmd := osexec.CommandContext(context.Background(), "/bin/sh", "-c", payload.Command)
				cmd.Stdout, cmd.Stderr = channel, channel.Stderr()
				status := uint32(0)
				if err := cmd.Run(); err != nil {
					if exitErr, ok := errors.AsType[*osexec.ExitError](err); ok {
						//nolint:gosec // process exit codes are 0..255
						status = uint32(exitErr.ExitCode())
					} else {
						status = 1
					}
				}
				//nolint:errcheck // the writer is the SSH channel under test
				fmt.Fprintf(channel.Stderr(), "")
				//nolint:errcheck,gosec // the client may already be gone
				channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
				return
			}
		}()
	}
}

// knownHostsFile writes the pinned host key line for the server.
func knownHostsFile(t *testing.T, s *sshTestServer) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	line := knownhosts.Line([]string{s.addr}, s.key)
	if werr := os.WriteFile(path, []byte(line+"\n"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	return path
}

// sshKeyFile writes an unencrypted ed25519 key so NewSSH has an auth method;
// the test DialClient replaces it with password auth.
func sshKeyFile(t *testing.T) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id_ed25519")
	block := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, block, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// passwordDialer is the shared test dialer: password auth with the pinned
// host-key callback from the backend config.
func passwordDialer(ctx context.Context, c *ssh.ClientConfig, addr string) (*ssh.Client, error) {
	cc := *c
	cc.Auth = []ssh.AuthMethod{ssh.Password("pw")}
	nc, err := netx.New(netx.Config{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("test dial: %w", err)
	}
	sc, chans, reqs, err := ssh.NewClientConn(nc, addr, &cc)
	if err != nil {
		//nolint:errcheck,gosec // the handshake already failed
		nc.Close()
		return nil, fmt.Errorf("test handshake: %w", err)
	}
	return ssh.NewClient(sc, chans, reqs), nil
}

func newTestSSH(t *testing.T, s *sshTestServer) *SSH {
	t.Helper()
	host, port, err := net.SplitHostPort(s.addr)
	if err != nil {
		t.Fatal(err)
	}
	p, aErr := strconv.Atoi(port)
	if aErr != nil {
		t.Fatal(aErr)
	}
	cfg := SSHConfig{
		Host: host, User: "tester", Port: p, KeyFile: sshKeyFile(t),
		KnownHosts: knownHostsFile(t, s), MaxConns: 2,
		KeepAlive: 50 * time.Millisecond, IdleClose: time.Minute,
		Dial:       netx.New(netx.Config{}).DialContext,
		DialClient: passwordDialer,
	}
	b, nerr := NewSSH(t.Context(), &cfg)
	if nerr != nil {
		t.Fatalf("NewSSH: %v", nerr)
	}
	t.Cleanup(func() {
		//nolint:errcheck,gosec // test cleanup
		b.Close(context.Background())
	})
	return b
}

func TestSSHContract(t *testing.T) {
	t.Parallel()
	srv := startSSHServer(t)
	b := newTestSSH(t, srv)

	res, err := b.Run(t.Context(), &exec.Spec{
		Argv: []string{"/bin/sh", "-c", "printf 'out:%s' \"$DAMNBOT_TEST\""},
		Dir:  t.TempDir(), Env: []string{"DAMNBOT_TEST=hello", "PATH=/usr/bin:/bin"}, Timeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != exec.StatusExited || !strings.Contains(string(res.Output), "out:hello") {
		t.Fatalf("status = %s output = %q", res.Status, res.Output)
	}

	res, err = b.Run(t.Context(), &exec.Spec{Argv: []string{"/bin/sh", "-c", "exit 7"}, Dir: "/tmp", Timeout: 10 * time.Second})
	if err != nil || res.Status != exec.StatusExited || res.ExitCode == nil || *res.ExitCode != 7 {
		t.Fatalf("exit: status=%s exit=%v err=%v", res.Status, res.ExitCode, err)
	}
	if res.ExecID == "" {
		t.Fatal("empty exec id")
	}
}

func TestSSHPoolReuse(t *testing.T) {
	t.Parallel()
	srv := startSSHServer(t)
	b := newTestSSH(t, srv)
	for i := range 20 {
		if _, err := b.Run(t.Context(), &exec.Spec{Argv: []string{"/bin/true"}, Dir: "/tmp", Timeout: 10 * time.Second}); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if got := srv.accepts.Load(); got != 1 {
		t.Fatalf("TCP accepts = %d, want 1 multiplexed connection", got)
	}
}

func TestSSHTimeoutAndSweep(t *testing.T) {
	t.Parallel()
	srv := startSSHServer(t)
	b := newTestSSH(t, srv)
	res, err := b.Run(t.Context(), &exec.Spec{
		Argv: []string{"/bin/sh", "-c", "sleep 30"}, Dir: "/tmp",
		Timeout: 300 * time.Millisecond, KillGrace: time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != exec.StatusTimeout {
		t.Fatalf("status = %s", res.Status)
	}
	if _, err := os.Stat(pidFile(res.ExecID)); !os.IsNotExist(err) {
		t.Fatalf("pid file survived cleanup: %v", err)
	}
	if killed, err := b.Sweep(t.Context(), res.ExecID); err != nil || killed != 0 {
		t.Fatalf("Sweep = %d, %v", killed, err)
	}
}

// TestSSHHostKeyPin: a known_hosts entry for another key rejects the
// handshake (no silent TOFU).
func TestSSHHostKeyPin(t *testing.T) {
	t.Parallel()
	srv := startSSHServer(t)
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherSigner, err := ssh.NewSignerFromKey(other)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "known_hosts")
	line := knownhosts.Line([]string{srv.addr}, otherSigner.PublicKey())
	if werr := os.WriteFile(path, []byte(line+"\n"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	host, port, sErr := net.SplitHostPort(srv.addr)
	if sErr != nil {
		t.Fatal(sErr)
	}
	p, aErr := strconv.Atoi(port)
	if aErr != nil {
		t.Fatal(aErr)
	}
	_, err = NewSSH(t.Context(), &SSHConfig{
		Host: host, User: "tester", Port: p, KeyFile: sshKeyFile(t), KnownHosts: path,
		Dial: netx.New(netx.Config{}).DialContext,
	})
	if err != nil {
		t.Fatalf("NewSSH: %v", err)
	}
	b := newTestSSHWithKnownHosts(t, srv, path)
	if _, err := b.Run(t.Context(), &exec.Spec{Argv: []string{"/bin/true"}, Dir: "/tmp", Timeout: 5 * time.Second}); err == nil || !strings.Contains(err.Error(), "key mismatch") {
		t.Fatalf("handshake = %v, want key mismatch", err)
	}
}

func newTestSSHWithKnownHosts(t *testing.T, s *sshTestServer, known string) *SSH {
	t.Helper()
	host, port, err := net.SplitHostPort(s.addr)
	if err != nil {
		t.Fatal(err)
	}
	p, aErr := strconv.Atoi(port)
	if aErr != nil {
		t.Fatal(aErr)
	}
	cfg := SSHConfig{
		Host: host, User: "tester", Port: p, KeyFile: sshKeyFile(t), KnownHosts: known,
		Dial:       netx.New(netx.Config{}).DialContext,
		DialClient: passwordDialer,
	}
	b, nerr := NewSSH(t.Context(), &cfg)
	if nerr != nil {
		t.Fatalf("NewSSH: %v", nerr)
	}
	t.Cleanup(func() {
		//nolint:errcheck,gosec // test cleanup
		b.Close(context.Background())
	})
	return b
}
