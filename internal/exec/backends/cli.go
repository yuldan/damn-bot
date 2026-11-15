//go:build !minimal

// Package backends implements the non-local exec backends of
// EXEC-AND-CODING §A.7 (ADR-275): docker, singularity, ssh, and the HTTP
// sandbox providers. Every backend is lazy (no daemon/connection work until
// the first Run), fails closed when its prerequisite is absent, and reports
// the capabilities it really enforces.
package backends

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	osexec "os/exec"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
)

// CLISpec is one CLI invocation of a backend: the full argv, the child
// environment (already allowlisted by the caller), stdin, and a cleanup
// function that removes any remote state the CLI created.
// CLISpec is one CLI invocation of a backend.
type CLISpec struct {
	argv    []string
	dir     string
	env     []string
	stdin   []byte
	timeout time.Duration
	grace   time.Duration
	// onOutput receives each chunk as it arrives (UI streaming).
	onOutput func([]byte)
	// cleanup runs after the CLI exits (success, timeout, or cancel); the
	// error is reported as a cleanup failure.
	cleanup func(context.Context) error
}

// RunCLI starts argv, streams combined output, and kills the tree within
// the Spec deadline. It is the shared transport of docker/singularity and
// the local command used by ssh's second channel.
func RunCLI(ctx context.Context, runner string, sp *CLISpec) (exec.Result, error) {
	res := exec.Result{ExecID: exec.NewExecID(), Runner: runner, Status: exec.StatusExited}
	start := time.Now()
	timeout := sp.timeout
	if timeout <= 0 {
		timeout = exec.DefaultTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out := &output{limit: exec.OutputCap, emit: sp.onOutput}
	newCmd := func() *osexec.Cmd {
		//nolint:gosec // argv comes from the approved Spec, not from untrusted interpolation
		c := osexec.CommandContext(callCtx, sp.argv[0], sp.argv[1:]...)
		c.Dir = sp.dir
		if len(sp.env) > 0 {
			c.Env = sp.env
		}
		if sp.stdin != nil {
			c.Stdin = bytes.NewReader(sp.stdin)
		}
		c.Stdout, c.Stderr = out, out
		prepareTree(c)
		return c
	}
	// ETXTBSY is transient: a process forked while the backend CLI was being
	// written (the test fixtures use write-then-rename) still holds the writer
	// until it execs. The command never started, so retrying the fork is safe
	// and keeps a capability probe from caching the transient failure in its
	// sync.Once (nightly 2026-11-10, TestSingularityRunContract).
	var cmd *osexec.Cmd
	if err := retrySpawn(ctx, func() error {
		cmd = newCmd()
		return cmd.Start()
	}); err != nil {
		res.Status, res.Duration = exec.StatusSpawnFailed, time.Since(start)
		return res, fmt.Errorf("%w: %s: %w", exec.ErrUnavailable, runner, err)
	}
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()

	var werr error
	timedOut, cancelled := false, false
	select {
	case werr = <-wait:
	case <-callCtx.Done():
		switch {
		case ctx.Err() != nil:
			cancelled = true
		default:
			timedOut = true
		}
		killTree(cmd, sp.grace)
		werr = <-wait
	}
	res.Duration = time.Since(start)
	res.Output, res.Truncated, res.BytesRaw = out.result()
	var cleanupErr error
	if sp.cleanup != nil {
		cleanupErr = sp.cleanup(context.WithoutCancel(ctx))
	}
	switch {
	case timedOut:
		res.Status = exec.StatusTimeout
		return res, cleanupErr
	case cancelled:
		res.Status = exec.StatusCancelledConfirmed
		return res, cleanupErr
	}
	if werr != nil {
		if exitErr, ok := errors.AsType[*osexec.ExitError](werr); ok {
			code := exitErr.ExitCode()
			res.ExitCode = &code
			res.Status = exec.StatusExited
			if sig := exitSignal(exitErr); sig != "" {
				res.Status, res.Signal = exec.StatusSignaled, sig
			}
			return res, cleanupErr
		}
		res.Status = exec.StatusSpawnFailed
		return res, errors.Join(fmt.Errorf("%w: %s: %w", exec.ErrUnavailable, runner, werr), cleanupErr)
	}
	zero := 0
	res.ExitCode = &zero
	return res, cleanupErr
}

// output accumulates combined output with head+tail truncation and streams
// each chunk to Spec.OnOutput.
type output struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	head    []byte
	bytes   int64
	dropped bool
	limit   int
	emit    func([]byte)
}

func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	o.bytes += int64(len(p))
	o.dropped = o.dropped || o.bytes > int64(o.limit)
	if o.bytes <= int64(o.limit) {
		o.buf.Write(p)
	} else if o.head == nil {
		half := o.limit / 2
		o.head = append([]byte(nil), o.buf.Bytes()[:half]...)
	}
	o.mu.Unlock()
	if o.emit != nil {
		o.emit(p)
	}
	return len(p), nil
}

func (o *output) result() ([]byte, bool, int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.dropped {
		return o.buf.Bytes(), false, o.bytes
	}
	// Keep the first and last halves of OutputCap.
	half := o.limit / 2
	data := o.head
	if data == nil {
		data = o.buf.Bytes()[:half]
	}
	if len(o.buf.Bytes()) > half {
		data = append(append([]byte(nil), data...), o.buf.Bytes()[len(o.buf.Bytes())-half:]...)
	}
	return data, true, o.bytes
}

// ResolveBinary finds a configured CLI in PATH (lazy: Run only).
func ResolveBinary(name string) (string, error) {
	if strings.ContainsRune(name, '/') {
		return name, nil
	}
	path, err := osexec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%w: %s not found in PATH", exec.ErrUnavailable, name)
	}
	return path, nil
}

// ShellQuote quotes one word for POSIX sh (single quotes, embedded quote
// escaped) so no argument can become shell syntax.
func ShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// EnvList filters the child environment: names from the allow list pass,
// and anything whose name looks like a credential is refused unless it is
// explicitly allowed (exec.env_allow). The token of a cloud backend never
// reaches this list.
func EnvList(env []string, allow []string) ([]string, error) {
	allowed := make(map[string]bool, len(allow))
	for _, name := range allow {
		allowed[name] = true
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("%w: malformed environment entry", exec.ErrSpec)
		}
		if secretName(name) && !allowed[name] {
			return nil, fmt.Errorf("%w: refusing to forward secret-like variable %s", exec.ErrCapability, name)
		}
		out = append(out, kv)
	}
	return out, nil
}

// secretName reports whether a variable name looks like a credential.
func secretName(name string) bool {
	up := strings.ToUpper(name)
	for _, frag := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "APIKEY", "API_KEY", "PRIVATE_KEY", "CREDENTIAL"} {
		if strings.Contains(up, frag) {
			return true
		}
	}
	return false
}

// sandboxExecution marks a result that did not run on the host (EC-TERM-003).
func sandboxExecution(backend, detail string) exec.Execution {
	return exec.Execution{Backend: backend, Sandboxed: true, Detail: detail}
}

// spawnAttempts bounds the ETXTBSY retries of one CLI fork; the backoff is
// 25+50+100+200 ms, short enough to stay inside a capability probe budget and
// long enough for the writer-holding fork to reach its own exec.
const spawnAttempts = 5

// retrySpawn calls start until it succeeds or the error is not ETXTBSY.
// start must build a fresh *exec.Cmd on every call: a Cmd whose Start failed
// is not reusable.
func retrySpawn(ctx context.Context, start func() error) error {
	var err error
	for attempt := range spawnAttempts {
		if attempt > 0 {
			t := time.NewTimer(25 * time.Millisecond << (attempt - 1))
			select {
			case <-ctx.Done():
				t.Stop()
				return fmt.Errorf("spawn retry: %w", ctx.Err())
			case <-t.C:
			}
		}
		if err = start(); err == nil || !isETXTBSY(err) {
			return err
		}
	}
	return err
}
