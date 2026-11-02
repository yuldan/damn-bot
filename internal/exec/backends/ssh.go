//go:build !minimal

package backends

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/damnbot/damnbot/internal/exec"
)

// SSHConfig is the composition-root view of [exec.backends.ssh].
type SSHConfig struct {
	Host       string
	User       string
	Port       int
	KeyFile    string
	KnownHosts string // required: host-key pinning, never TOFU
	EnvAllow   []string
	KeepAlive  time.Duration
	IdleClose  time.Duration
	Timeout    time.Duration
	MaxConns   int
	Env        []string // process environment, used only to find SSH_AUTH_SOCK
	// Dial dials TCP through the caller's policy (netx); nil fails closed.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// DialClient is a test seam for the handshake; nil uses Dial.
	DialClient func(ctx context.Context, cfg *ssh.ClientConfig, addr string) (*ssh.Client, error)
}

// SSH runs commands on a remote host over one multiplexed connection per
// (user, host, port), with a mandatory known_hosts check.
type SSH struct {
	cfg  SSHConfig
	pool *sshPool
}

var _ exec.Backend = (*SSH)(nil)

// NewSSH validates the pinned host key file and auth material.
func NewSSH(ctx context.Context, cfg *SSHConfig) (*SSH, error) {
	if cfg.Host == "" || cfg.User == "" {
		return nil, fmt.Errorf("%w: ssh host and user are required", exec.ErrSpec)
	}
	if cfg.Port == 0 {
		cfg.Port = 22
	}
	if cfg.MaxConns == 0 {
		cfg.MaxConns = 4
	}
	if cfg.KeepAlive == 0 {
		cfg.KeepAlive = 30 * time.Second
	}
	if cfg.IdleClose == 0 {
		cfg.IdleClose = 10 * time.Minute
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = exec.DefaultTimeout
	}
	hk, err := knownhosts.New(cfg.KnownHosts)
	if err != nil {
		return nil, fmt.Errorf("%w: ssh known_hosts: %w", exec.ErrUnavailable, err)
	}
	if cfg.Dial == nil {
		return nil, fmt.Errorf("%w: ssh needs a dialer (netx)", exec.ErrUnavailable)
	}
	auth, err := sshAuth(ctx, cfg)
	if err != nil {
		return nil, err
	}
	clientCfg := &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auth,
		HostKeyCallback: hk,
		Timeout:         cfg.Timeout,
	}
	cfg.KnownHosts = "" // parsed; keep no path to reopen silently
	return &SSH{cfg: *cfg, pool: newSSHPool(cfg, clientCfg)}, nil
}

// ID is the backend id.
func (s *SSH) ID() string { return "ssh" }

// Caps reports that remote commands get signals and the two-channel tree kill; the
// filesystem is not shared (no SharesFS), and resource limits/PTY/sessions
// are not emulated.
func (s *SSH) Caps() exec.Caps {
	return exec.Caps{Signals: true}
}

// Run executes one remote command. The remote wrapper records its pid so a
// timeout kills the whole process group over a second channel.
//
//nolint:funlen,gocyclo,errcheck,gosec // one state machine; teardown errors are not actionable after a result
func (s *SSH) Run(ctx context.Context, spec *exec.Spec) (exec.Result, error) {
	if spec.PTY {
		return exec.Result{Status: exec.StatusSpawnFailed}, fmt.Errorf("%w: ssh: pty", exec.ErrCapability)
	}
	if spec.Limits.Set() {
		return exec.Result{Status: exec.StatusSpawnFailed}, fmt.Errorf("%w: ssh: resource_limits", exec.ErrCapability)
	}
	env, err := EnvList(spec.Env, s.cfg.EnvAllow)
	if err != nil {
		return exec.Result{Status: exec.StatusSpawnFailed}, err
	}
	id := exec.NewExecID()
	res := exec.Result{ExecID: id, Runner: s.ID(), Status: exec.StatusExited, Execution: sandboxExecution(s.ID(), "remote host replaces local execution")}
	client, release, err := s.pool.acquire(ctx)
	if err != nil {
		res.Status = exec.StatusSpawnFailed
		return res, err
	}
	defer release()
	start := time.Now()
	session, err := client.NewSession()
	if err != nil {
		res.Status, res.Duration = exec.StatusSpawnFailed, time.Since(start)
		return res, fmt.Errorf("%w: ssh session: %w", exec.ErrUnavailable, err)
	}
	defer session.Close()
	out := &output{limit: exec.OutputCap, emit: spec.OnOutput}
	session.Stdout, session.Stderr = out, out
	if spec.Stdin != nil {
		session.Stdin = bytes.NewReader(spec.Stdin)
	}
	command := remoteCommand(id, spec.Dir, env, spec.Argv)
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = s.cfg.Timeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()
	var werr error
	timedOut, cancelled := false, false
	select {
	case werr = <-done:
	case <-callCtx.Done():
		if ctx.Err() != nil {
			cancelled = true
		} else {
			timedOut = true
		}
		session.Signal(ssh.SIGTERM)
		session.Close()
		select {
		case werr = <-done:
		case <-time.After(spec.KillGrace):
			session.Signal(ssh.SIGKILL)
			werr = <-done
		}
	}
	cleanupErr := s.killRemote(context.WithoutCancel(ctx), client, id)
	res.Duration = time.Since(start)
	res.Output, res.Truncated, res.BytesRaw = out.result()
	switch {
	case timedOut:
		res.Status = exec.StatusTimeout
		return res, cleanupErr
	case cancelled:
		res.Status = exec.StatusCancelledConfirmed
		return res, cleanupErr
	}
	if werr != nil {
		if exitErr, ok := errors.AsType[*ssh.ExitError](werr); ok {
			code := exitErr.ExitStatus()
			res.ExitCode = &code
			if sig := exitErr.Signal(); sig != "" {
				res.Status, res.Signal = exec.StatusSignaled, sig
			}
			return res, cleanupErr
		}
		res.Status = exec.StatusSpawnFailed
		return res, errors.Join(fmt.Errorf("%w: ssh: %w", exec.ErrUnavailable, werr), cleanupErr)
	}
	zero := 0
	res.ExitCode = &zero
	return res, cleanupErr
}

// Sweep kills the pid recorded for execID and reports whether it did.
func (s *SSH) Sweep(ctx context.Context, execID string) (int, error) {
	if execID == "" {
		return 0, fmt.Errorf("%w: empty exec id", exec.ErrSpec)
	}
	client, release, err := s.pool.acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	script := "f=" + ShellQuote(pidFile(execID)) + `; if [ -f "$f" ]; then p=$(cat "$f"); kill -KILL "-$p" 2>/dev/null || kill -KILL "$p" 2>/dev/null; rm -f "$f"; echo 1; else echo 0; fi`
	out, err := s.runShort(ctx, client, script)
	if err != nil {
		return 0, err
	}
	if strings.TrimSpace(out) == "1" {
		return 1, nil
	}
	return 0, nil
}

// Close closes pooled connections; each Run kills its own remote tree.
func (s *SSH) Close(context.Context) error { return s.pool.closeAll() }

// killRemote reads the recorded pid and kills its group; a missing file is
// already-clean state.
func (s *SSH) killRemote(ctx context.Context, client *ssh.Client, id string) error {
	// The main channel already received TERM (and a grace period); the
	// cleanup channel sends KILL to the recorded process group.
	script := "f=" + ShellQuote(pidFile(id)) + `; if [ -f "$f" ]; then p=$(cat "$f"); kill -KILL "-$p" 2>/dev/null || kill -KILL "$p" 2>/dev/null; rm -f "$f"; fi`
	_, err := s.runShort(ctx, client, script)
	return err
}

//nolint:errcheck,gosec // teardown after a result is not actionable
func (s *SSH) runShort(ctx context.Context, client *ssh.Client, script string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("exec: ssh cleanup session: %w", err)
	}
	defer session.Close()
	buf := &lockedBuffer{}
	session.Stdout, session.Stderr = buf, buf
	done := make(chan error, 1)
	go func() { done <- session.Run(script) }()
	select {
	case err := <-done:
		return buf.String(), err
	case <-ctx.Done():
		session.Close()
		return buf.String(), fmt.Errorf("exec: ssh cleanup: %w", ctx.Err())
	}
}

// lockedBuffer is a concurrent-safe output sink for the two SSH channel
// readers (x/crypto/ssh copies stdout and stderr in parallel).
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	if err != nil {
		return n, fmt.Errorf("exec: ssh output: %w", err)
	}
	return n, nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// remoteCommand builds a POSIX command that records the pid, sets cwd/env,
// and execs argv without shell interpolation of arguments. The pid is
// written inside the setsid child so the recorded pid is the group leader.
func remoteCommand(id, cwd string, env, argv []string) string {
	body := "echo $$ > " + ShellQuote(pidFile(id)) + "; cd \"$1\" || exit 1; shift; exec \"$@\""
	var suffix strings.Builder
	suffix.WriteString("env -i")
	for _, kv := range env {
		suffix.WriteString(" " + ShellQuote(kv))
	}
	suffix.WriteString(" sh -c " + ShellQuote(body) + " sh " + ShellQuote(cwd))
	for _, arg := range argv {
		suffix.WriteString(" " + ShellQuote(arg))
	}
	// setsid makes the command a process-group leader, so the cleanup
	// channel can kill the whole remote tree; without it the leader alone
	// is killed (documented limitation of minimal remote images).
	return "if command -v setsid >/dev/null 2>&1; then exec setsid " + suffix.String() +
		"; else exec " + suffix.String() + "; fi"
}

func pidFile(id string) string { return "/tmp/damnbot-exec-" + id + ".pid" }

type sshConn struct {
	client *ssh.Client
	last   time.Time
	stop   chan struct{}
}

// sshPool keeps one *ssh.Client per (user, host, port); keepalive detects a
// dead link and IdleClose bounds an unused connection.
type sshPool struct {
	cfg    *SSHConfig
	client *ssh.ClientConfig
	sem    chan struct{}

	mu    sync.Mutex
	conns map[string]*sshConn
}

func newSSHPool(cfg *SSHConfig, client *ssh.ClientConfig) *sshPool {
	return &sshPool{cfg: cfg, client: client, sem: make(chan struct{}, cfg.MaxConns), conns: map[string]*sshConn{}}
}

func (p *sshPool) key() string { return net.JoinHostPort(p.cfg.Host, strconv.Itoa(p.cfg.Port)) }

// acquire returns a live client and a release function that marks it idle.
func (p *sshPool) acquire(ctx context.Context) (*ssh.Client, func(), error) {
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, fmt.Errorf("exec: ssh acquire: %w", ctx.Err())
	}
	release := func() {
		p.mu.Lock()
		if c := p.conns[p.key()]; c != nil {
			c.last = time.Now()
		}
		p.mu.Unlock()
		<-p.sem
	}
	p.mu.Lock()
	c := p.conns[p.key()]
	if c != nil && time.Since(c.last) > p.cfg.IdleClose {
		close(c.stop)
		//nolint:errcheck,gosec // best-effort close of a stale connection
		c.client.Close()
		delete(p.conns, p.key())
		c = nil
	}
	p.mu.Unlock()
	if c != nil {
		return c.client, release, nil
	}
	client, err := p.dial(ctx)
	if err != nil {
		<-p.sem
		return nil, nil, err
	}
	conn := &sshConn{client: client, last: time.Now(), stop: make(chan struct{})}
	p.mu.Lock()
	p.conns[p.key()] = conn
	p.mu.Unlock()
	go p.keepAlive(conn)
	return client, release, nil
}

func (p *sshPool) dial(ctx context.Context) (*ssh.Client, error) {
	addr := p.key()
	if p.cfg.DialClient != nil {
		return p.cfg.DialClient(ctx, p.client, addr)
	}
	nc, err := p.cfg.Dial(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("%w: ssh dial %s: %w", exec.ErrUnavailable, addr, err)
	}
	cc, chans, reqs, err := ssh.NewClientConn(nc, addr, p.client)
	if err != nil {
		//nolint:errcheck,gosec // the handshake already failed
		nc.Close()
		return nil, fmt.Errorf("%w: ssh handshake %s: %w", exec.ErrUnavailable, addr, err)
	}
	return ssh.NewClient(cc, chans, reqs), nil
}

// keepAlive sends a keepalive request; a failure drops the connection so
// the next call redials instead of reusing a dead link.
func (p *sshPool) keepAlive(c *sshConn) {
	t := time.NewTicker(p.cfg.KeepAlive)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
			if _, _, err := c.client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
				p.mu.Lock()
				if p.conns[p.key()] == c {
					delete(p.conns, p.key())
				}
				p.mu.Unlock()
				//nolint:errcheck,gosec // keepalive already proved the link dead
				c.client.Close()
				return
			}
		}
	}
}

func (p *sshPool) closeAll() error {
	p.mu.Lock()
	conns := p.conns
	p.conns = map[string]*sshConn{}
	p.mu.Unlock()
	var errs error
	for _, c := range conns {
		close(c.stop)
		errs = errors.Join(errs, c.client.Close())
	}
	return errs
}

// sshAuth picks key-file or SSH agent authentication; no password and no
// keyboard-interactive (a remote prompt would hang the pipe runner).
func sshAuth(ctx context.Context, cfg *SSHConfig) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if cfg.KeyFile != "" {
		raw, err := os.ReadFile(cfg.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("%w: ssh key: %w", exec.ErrUnavailable, err)
		}
		signer, err := ssh.ParsePrivateKey(raw)
		if err != nil {
			return nil, fmt.Errorf("%w: ssh key (encrypted keys are unsupported): %w", exec.ErrUnavailable, err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if agent := agentAuth(ctx, cfg.Env, cfg.Dial); agent != nil {
		methods = append(methods, agent)
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("%w: ssh needs key_file or an available ssh-agent", exec.ErrUnavailable)
	}
	return methods, nil
}
