package exec

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Persistent sessions (§A.2): one long-lived shell per name; commands are
// sourced from 0600 files and end with a per-command nonce sentinel.
const (
	MaxSessions     = 4
	SessionIdleReap = 30 * time.Minute
	// sessionInterruptWait: after interrupting the foreground job, the time
	// the sentinel may take before the shell is killed and recreated.
	sessionInterruptWait = 3 * time.Second
	sentinelMark         = '\x1e'
)

// Session errors.
var (
	// ErrSessionLimit: a 5th session name was requested (§A.2).
	ErrSessionLimit = errors.New("exec: session limit reached")
	// ErrSessionName rejects a name outside ^[a-z0-9_-]{1,32}$.
	ErrSessionName = errors.New("exec: invalid session name")
)

// Sessions holds the persistent shells of one agent session.
type Sessions struct {
	r   *Runner
	dir string   // <state_dir>/exec/sess
	env []string // child environment, built without secret refs (ADR-088 butir 4)
	now func() time.Time

	mu sync.Mutex
	m  map[string]*session
}

// NewSessions returns the session table of one agent session; command
// files go under dir, shells get env (built without direct argv0, so
// without secret refs).
func (r *Runner) NewSessions(dir string, env []string) *Sessions {
	return &Sessions{r: r, dir: dir, env: env, now: time.Now, m: map[string]*session{}}
}

// session is one live shell.
type session struct {
	mu      sync.Mutex // one command at a time
	c       *call      // the shell process (call state; finished by kill)
	sh      sessionShellSpec
	input   *os.File // PTY master or the write end of the stdin pipe
	dir     string
	n       int
	lastUse time.Time
	killed  bool
}

// sessionShellSpec is the per-OS shell dialect of a session.
type sessionShellSpec struct {
	argv  []string
	pty   bool
	ext   string                          // command file extension
	setup string                          // first command: echo, prompts, aliases off
	line  func(path, nonce string) string // runs path, then prints the sentinel
	again func(nonce string) string       // the sentinel alone, status 130
}

// Exec runs command in session name, creating the shell on first use.
func (ss *Sessions) Exec(ctx context.Context, name, command string, stdin []byte, timeout time.Duration) (Result, error) {
	if !validSessionName(name) {
		return Result{}, ErrSessionName
	}
	s, err := ss.get(ctx, name)
	if err != nil {
		return Result{Status: StatusSpawnFailed}, err
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	s.mu.Lock()
	res, reset, err := s.run(ctx, command, stdin, timeout)
	s.lastUse = ss.now()
	s.mu.Unlock()
	if reset {
		res.SessionReset = true
		err = errors.Join(err, ss.drop(name))
	}
	return res, err
}

// Close kills every session shell.
func (ss *Sessions) Close() error {
	ss.mu.Lock()
	names := make([]string, 0, len(ss.m))
	for n := range ss.m {
		names = append(names, n)
	}
	ss.mu.Unlock()
	var err error
	for _, n := range names {
		err = errors.Join(err, ss.drop(n))
	}
	return err
}

func (ss *Sessions) get(ctx context.Context, name string) (*session, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	now := ss.now()
	for n, s := range ss.m { // idle reap (§A.2)
		if n != name && now.Sub(s.lastUse) >= SessionIdleReap {
			delete(ss.m, n)
			s.kill()
		}
	}
	if s, ok := ss.m[name]; ok {
		return s, nil
	}
	if len(ss.m) >= MaxSessions {
		return nil, ErrSessionLimit
	}
	s, err := ss.start(ctx)
	if err != nil {
		return nil, err
	}
	s.lastUse = now
	ss.m[name] = s
	return s, nil
}

func (ss *Sessions) drop(name string) error {
	ss.mu.Lock()
	s, ok := ss.m[name]
	delete(ss.m, name)
	ss.mu.Unlock()
	if !ok {
		return nil
	}
	s.kill()
	if err := os.RemoveAll(s.dir); err != nil {
		return fmt.Errorf("exec: session files: %w", err)
	}
	return s.c.err()
}

// start launches the session shell and runs its setup command.
func (ss *Sessions) start(ctx context.Context) (*session, error) {
	sh := sessionShell(envPath(ss.env))
	c := &call{
		r: ss.r, id: newExecID(), out: newCapture(),
		read: make(chan struct{}), exit: make(chan exit, 1), more: make(chan struct{}, 1),
	}
	dir := filepath.Join(ss.dir, c.id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("exec: session dir: %w", err)
	}
	c.s = Spec{Argv: sh.argv, Dir: dir, PTY: sh.pty, Env: withMarker(ss.env, c.id), KillGrace: time.Second}
	c.start = time.Now()
	var inW *os.File
	if !sh.pty {
		var err error
		if c.in, inW, err = os.Pipe(); err != nil {
			return nil, errors.Join(fmt.Errorf("exec: session pipe: %w", err), os.RemoveAll(dir))
		}
	}
	if err := c.spawn(ctx); err != nil {
		return nil, errors.Join(err, closeIf(inW != nil, inW), os.RemoveAll(dir))
	}
	s := &session{c: c, sh: sh, dir: dir, input: inW}
	if sh.pty {
		s.input = c.p.output()
	}
	if _, reset, err := s.run(ctx, sh.setup, nil, 10*time.Second); err != nil || reset {
		s.kill()
		return nil, errors.Join(errors.New("exec: session setup failed"), err, os.RemoveAll(dir))
	}
	return s, nil
}

// run sends one command and waits for its sentinel. reset reports that the
// shell was killed (timeout without sentinel, prompt, cancel, shell exit).
func (s *session) run(ctx context.Context, command string, stdin []byte, timeout time.Duration) (Result, bool, error) {
	res := Result{Runner: RunnerPipe, ExecID: s.c.id}
	if s.sh.pty {
		res.Runner = RunnerPTY
	}
	start := time.Now()
	s.n++
	path := filepath.Join(s.dir, "c"+strconv.Itoa(s.n)+s.sh.ext)
	if err := os.WriteFile(path, []byte(command+"\n"), 0o600); err != nil {
		return res, false, fmt.Errorf("exec: session command file: %w", err)
	}
	nonce := newNonce()
	from := s.c.out.size()
	if _, err := s.input.WriteString(s.sh.line(path, nonce)); err != nil {
		return res, true, fmt.Errorf("exec: session write: %w", err)
	}
	if len(stdin) > 0 {
		if _, err := s.input.Write(stdin); err != nil {
			return res, true, fmt.Errorf("exec: session stdin: %w", err)
		}
	}
	reset := s.wait(ctx, nonce, from, timeout, &res)
	res.Output = stripFrames(res.Output)
	res.Duration = time.Since(start)
	return res, reset, nil
}

// wait follows the output until the sentinel, a prompt, the timeout, a
// cancel, or the shell's exit.
func (s *session) wait(ctx context.Context, nonce string, from int64, timeout time.Duration, res *Result) bool {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(watchEvery)
	defer tick.Stop()
	var interrupted time.Time
	for {
		if out, code, cwd, ok := s.sentinel(nonce, from); ok {
			res.Output, res.Cwd = out, cwd
			res.Status, res.ExitCode = StatusExited, &code
			if !interrupted.IsZero() {
				res.Status, res.ExitCode = StatusTimeout, nil
			}
			return false
		}
		select {
		case <-s.c.more:
		case <-tick.C:
		case <-deadline.C:
			s.interrupt(nonce)
			interrupted = time.Now()
		case <-ctx.Done():
			s.kill()
			res.Status, res.Output = StatusCancelledConfirmed, s.since(from)
			return true
		case ex := <-s.c.exit:
			s.c.exit <- ex // kept for kill/finish
			res.Status, res.Output = StatusExited, s.since(from)
			return true
		}
		if st, line, kind := s.check(from, interrupted); st != "" {
			res.Status, res.InputKind, res.Output = st, kind, s.since(from)
			res.Prompt = sanitizePrompt(s.c.r.redactor(), line)
			s.kill()
			return true
		}
	}
}

// check reports a timeout whose interrupt did not end the job, or a
// prompt (§A.6) waiting for input.
func (s *session) check(from int64, interrupted time.Time) (st Status, line, kind string) {
	if !interrupted.IsZero() && time.Since(interrupted) >= sessionInterruptWait {
		return StatusTimeout, "", ""
	}
	last := time.Unix(0, s.c.lastOut.Load())
	if time.Since(last) < promptSilence || s.c.out.size() == from {
		return "", "", ""
	}
	line = lastLine(s.c.out.tail(promptTail))
	if kind = promptKind(s.c.r.prompts, line); kind != "" {
		return StatusWaitingForInput, line, kind
	}
	return "", "", ""
}

// sentinel looks for "\x1eDB:<nonce>:<code>:<cwd>\x1e" after from.
func (s *session) sentinel(nonce string, from int64) ([]byte, int, string, bool) {
	out := s.since(from)
	before, rest, found := bytes.Cut(out, []byte(string(sentinelMark)+"DB:"+nonce+":"))
	if !found {
		return nil, 0, "", false
	}
	body, _, closed := bytes.Cut(rest, []byte{sentinelMark})
	if !closed {
		return nil, 0, "", false
	}
	codeS, cwd, ok := strings.Cut(string(body), ":")
	code, err := strconv.Atoi(strings.TrimSpace(codeS))
	if !ok || err != nil {
		return nil, 0, "", false
	}
	return before, code, strings.TrimSpace(cwd), true
}

// stripFrames removes session protocol frames
// ("\x1eDB:<nonce>:<code>:<cwd>\x1e") from command output. After an
// interrupt a shell may run both the command-line sentinel and the queued
// interrupt sentinel; the second frame can surface while the next command
// runs, so protocol bytes never reach the caller (EX63-1).
func stripFrames(out []byte) []byte {
	if bytes.IndexByte(out, sentinelMark) < 0 {
		return out
	}
	var b []byte
	rest := out
	for {
		i := bytes.IndexByte(rest, sentinelMark)
		if i < 0 {
			break
		}
		j := bytes.IndexByte(rest[i+1:], sentinelMark)
		if j < 0 {
			break
		}
		if !frameBody(rest[i+1 : i+1+j]) {
			b = append(b, rest[:i+1]...)
			rest = rest[i+1:]
			continue
		}
		b = append(b, rest[:i]...)
		rest = rest[i+1+j+1:]
	}
	return append(b, rest...)
}

// frameBody reports whether body (between two sentinel marks) has the
// "DB:<32 hex nonce>:<code>:<cwd>" shape of a protocol frame.
func frameBody(body []byte) bool {
	body, ok := bytes.CutPrefix(body, []byte("DB:"))
	if !ok {
		return false
	}
	nonce, rest, ok := bytes.Cut(body, []byte(":"))
	if !ok || len(nonce) != 32 {
		return false
	}
	for _, c := range nonce {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	code, _, ok := bytes.Cut(rest, []byte(":"))
	if !ok {
		return false
	}
	_, err := strconv.Atoi(strings.TrimSpace(string(code)))
	return err == nil
}

// since returns the output after offset from (the command's own output),
// with CRLF line ends as LF.
func (s *session) since(from int64) []byte {
	all, _, total := s.c.out.bytes()
	n := min(int(total-from), len(all))
	return bytes.ReplaceAll(all[len(all)-n:], []byte("\r\n"), []byte("\n"))
}

// interrupt stops the foreground job (§A.2: SIGINT on Unix) and sends the
// sentinel again: an interactive shell abandons the rest of an interrupted
// line. The sentinel runs once the job has ended.
func (s *session) interrupt(nonce string) {
	if err := interruptForeground(s); err != nil {
		s.c.note(fmt.Errorf("exec: session interrupt: %w", err))
	}
	if _, err := s.input.WriteString(s.sh.again(nonce)); err != nil {
		s.c.note(fmt.Errorf("exec: session write: %w", err))
	}
}

// kill ends the shell tree once (kill order, sweep, drain).
func (s *session) kill() {
	if s.killed {
		return
	}
	s.killed = true
	s.c.killTree(s.c.s.KillGrace)
	s.c.finish()
	if !s.sh.pty {
		s.c.note(ignoreClosed(s.input.Close()))
	}
}

func newNonce() string {
	var b [16]byte
	_, _ = rand.Read(b[:]) // crypto/rand never fails on supported platforms
	return hex.EncodeToString(b[:])
}

func validSessionName(n string) bool {
	if n == "" || len(n) > 32 {
		return false
	}
	for _, c := range n {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}
