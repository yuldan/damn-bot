//go:build unix

package e2e

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/netx"
)

// serveProc is one real `damnbot serve` child. stdout carries the bound
// observability address; stderr carries lifecycle and servetest lines.
type serveProc struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdout *lockedBuffer
	stderr *lockedBuffer
	addr   string
	wait   chan error
	exited chan struct{}
}

var serveAddrRE = regexp.MustCompile(`observability listening on ([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+:[0-9]+)`)

func startServe(t *testing.T, e *Env, bin string, env []string, extra ...string) *serveProc {
	t.Helper()
	args := append([]string{"serve", "--config", e.cfg, "--data-dir", e.DataDir, "--bind", "127.0.0.1:0"}, extra...)
	cmd := exec.CommandContext(context.Background(), bin, args...) //nolint:gosec // binary is this package's temp build
	cmd.Dir = e.Workdir
	cmd.Env = append(e.childEnv(), env...)
	p := &serveProc{
		t:      t,
		cmd:    cmd,
		stdout: &lockedBuffer{},
		stderr: &lockedBuffer{},
		wait:   make(chan error, 1),
		exited: make(chan struct{}),
	}
	cmd.Stdout, cmd.Stderr = p.stdout, p.stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start serve: %v", err)
	}
	go func() {
		p.wait <- cmd.Wait()
		close(p.exited)
	}()
	t.Cleanup(p.stop)
	p.addr = p.awaitAddr(10 * time.Second)
	return p
}

// awaitAddr polls the child's stdout for the listening line.
func (p *serveProc) awaitAddr(limit time.Duration) string {
	p.t.Helper()
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if m := serveAddrRE.FindStringSubmatch(p.stdout.String()); m != nil {
			return m[1]
		}
		select {
		case <-p.exited:
			p.t.Fatalf("serve exited before listening\nstdout:\n%s\nstderr:\n%s", p.stdout.String(), p.stderr.String())
		case <-deadline.C:
			p.t.Fatalf("serve did not report a listener in %s\nstdout:\n%s\nstderr:\n%s", limit, p.stdout.String(), p.stderr.String())
		case <-tick.C:
		}
	}
}

// awaitLog waits until w contains want.
func (p *serveProc) awaitLog(buf *lockedBuffer, want string, limit time.Duration) {
	p.t.Helper()
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if strings.Contains(buf.String(), want) {
			return
		}
		select {
		case <-p.exited:
			p.t.Fatalf("serve exited while waiting for %q\nstderr:\n%s", want, p.stderr.String())
		case <-deadline.C:
			p.t.Fatalf("serve did not log %q in %s\nstderr:\n%s", want, limit, p.stderr.String())
		case <-tick.C:
		}
	}
}

func (p *serveProc) url(path string) string { return "http://" + p.addr + path }

// awaitState waits for a servetest sentinel. The file is the synchronization
// point (written before the turn releases its slot); the deadline only bounds
// a broken child, it is never the source of truth.
func awaitState(t *testing.T, p *serveProc, state, name string, limit time.Duration) {
	t.Helper()
	path := filepath.Join(state, name)
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-p.exited:
			t.Fatalf("serve exited before writing %s\nstderr:\n%s", name, p.stderr.String())
		case <-deadline.C:
			t.Fatalf("servetest state %s not written in %s\nstderr:\n%s", name, limit, p.stderr.String())
		case <-tick.C:
		}
	}
}

func stateExists(state, name string) bool {
	_, err := os.Stat(filepath.Join(state, name))
	return err == nil
}

func (p *serveProc) terminate() {
	p.t.Helper()
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		p.t.Fatalf("SIGTERM: %v", err)
	}
}

// waitExit waits for termination and returns the exit code.
func (p *serveProc) waitExit(limit time.Duration) int {
	p.t.Helper()
	select {
	case err := <-p.wait:
		return exitCode(p.t, err)
	case <-time.After(limit):
		if kerr := p.cmd.Process.Kill(); kerr != nil && !errors.Is(kerr, os.ErrProcessDone) {
			p.t.Logf("kill after exit timeout: %v", kerr)
		}
		p.t.Fatalf("serve did not exit within %s; stderr:\n%s", limit, p.stderr.String())
		return -1
	}
}

// stop is the cleanup path: terminate if still running, kill as a backstop.
func (p *serveProc) stop() {
	select {
	case <-p.exited:
		return
	default:
	}
	if serr := p.cmd.Process.Signal(syscall.SIGTERM); serr != nil && !errors.Is(serr, os.ErrProcessDone) {
		p.t.Logf("SIGTERM during cleanup: %v", serr)
	}
	select {
	case <-p.exited:
	case <-time.After(10 * time.Second):
		if kerr := p.cmd.Process.Kill(); kerr != nil && !errors.Is(kerr, os.ErrProcessDone) {
			p.t.Logf("kill during cleanup: %v", kerr)
		}
		<-p.exited
	}
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return exit.ExitCode()
	}
	t.Fatalf("wait: %v", err)
	return -1
}

// obsGet performs one loopback GET through netx (E-05: no bare HTTP client
// outside netx, and a test-binary Network dials loopback only).
func obsGet(t *testing.T, url string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request %s: %v", url, err)
	}
	network := netx.New(netx.Config{Offline: true})
	resp, err := network.Client("inference", netx.Inference).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	cerr := resp.Body.Close()
	if err := errors.Join(rerr, cerr); err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return resp.StatusCode, string(body)
}

func TestServeObservabilityAndCleanStop(t *testing.T) {
	e := newEnv(t)
	p := startServe(t, e, binPaths.main, nil)

	if code, body := obsGet(t, p.url("/healthz")); code != http.StatusOK || body != "ok\n" {
		t.Fatalf("/healthz = (%d, %q)", code, body)
	}
	if code, body := obsGet(t, p.url("/readyz")); code != http.StatusOK || !strings.Contains(body, `"status":"ok"`) {
		t.Fatalf("/readyz = (%d, %q)", code, body)
	}
	code, body := obsGet(t, p.url("/metrics"))
	if code != http.StatusOK {
		t.Fatalf("/metrics = %d %q", code, body)
	}
	for _, want := range []string{
		"damnbot_uptime_seconds",
		"damnbot_turn_latency_seconds",
		"damnbot_turns_total",
		"damnbot_errors_total",
		"damnbot_queue_depth",
		"damnbot_lease_held 1",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics lacks %q:\n%s", want, body)
		}
	}

	start := time.Now()
	p.terminate()
	if code := p.waitExit(5 * time.Second); code != 0 {
		t.Fatalf("idle SIGTERM exit = %d, want 0\nstderr:\n%s", code, p.stderr.String())
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("idle shutdown took %s", elapsed)
	}
}

// TestServeTerminatesCleanlyRightAfterListening is the regression for the
// signal-handler race: SIGTERM sent as soon as the child announced its
// listener used to arrive before the handler was installed, so the default
// disposition killed the child and waitExit saw a signal status (-1).
func TestServeTerminatesCleanlyRightAfterListening(t *testing.T) {
	e := newEnv(t)
	p := startServe(t, e, binPaths.main, nil)
	p.terminate()
	if code := p.waitExit(10 * time.Second); code != 0 {
		t.Fatalf("SIGTERM right after listening = %d, want 0\nstderr:\n%s", code, p.stderr.String())
	}
}

func TestServeSecondInstanceRefused(t *testing.T) {
	e := newEnv(t)
	p := startServe(t, e, binPaths.main, nil)

	res := e.Exec("serve", "--config", e.cfg, "--data-dir", e.DataDir, "--bind", "127.0.0.1:0")
	if res.Code != 78 {
		t.Fatalf("second instance exit = %d, want 78\nstderr:\n%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "already owns this data dir") || !strings.Contains(res.Stderr, "pid ") {
		t.Errorf("second instance message lacks the owner: %q", res.Stderr)
	}
	if code, body := obsGet(t, p.url("/healthz")); code != http.StatusOK {
		t.Fatalf("first instance unhealthy after refusal: %d %q", code, body)
	}
}

func TestServeDrainCompletesActiveTurn(t *testing.T) {
	e := newEnv(t)
	state := filepath.Join(t.TempDir(), "servetest-state")
	p := startServe(t, e, binPaths.serve, []string{"DAMNBOT_SERVE_TEST_TURN=2s", "DAMNBOT_SERVE_TEST_STATE=" + state})
	awaitState(t, p, state, "turn-started", 10*time.Second)

	p.terminate()
	if code := p.waitExit(10 * time.Second); code != 0 {
		t.Fatalf("drained exit = %d, want 0\nstderr:\n%s", code, p.stderr.String())
	}
	// The hook writes the marker before the turn releases its slot, and the
	// drain waits for that slot, so the completed marker must already exist.
	awaitState(t, p, state, "turn-completed", time.Second)
	if stateExists(state, "turn-cancelled") {
		t.Errorf("active turn was cancelled inside the drain window:\n%s", p.stderr.String())
	}
}

// TestServeDrainNegativeControl uses a 1 ms window: the same 2 s turn is
// cancelled instead of completing. Without the drain wait the "completed"
// assertion above could never hold, so this control is load-bearing.
func TestServeDrainNegativeControl(t *testing.T) {
	e := newEnv(t)
	state := filepath.Join(t.TempDir(), "servetest-state")
	p := startServe(t, e, binPaths.serve, []string{"DAMNBOT_SERVE_TEST_TURN=2s", "DAMNBOT_SERVE_TEST_STATE=" + state}, "--drain-timeout=1ms")
	awaitState(t, p, state, "turn-started", 10*time.Second)
	p.terminate()
	if code := p.waitExit(10 * time.Second); code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr:\n%s", code, p.stderr.String())
	}
	awaitState(t, p, state, "turn-cancelled", time.Second)
	if stateExists(state, "turn-completed") {
		t.Errorf("control turn completed without a drain window:\n%s", p.stderr.String())
	}
}

// TestECTERM008ShutdownBackstop pins "EC-TERM-008" (EDGE-CASES-hermes.md): a
// turn that ignores its context must not hold the process past the cancel
// grace. The servetest hook runs for 30 s whatever happens to its context;
// after the 5 s grace serve must log the stuck turn and force-exit cleanly
// within grace+1 s, leaving no turn goroutine behind in the child.
func TestECTERM008ShutdownBackstop(t *testing.T) {
	e := newEnv(t)
	state := filepath.Join(t.TempDir(), "servetest-state")
	p := startServe(t, e, binPaths.serve, []string{"DAMNBOT_SERVE_TEST_TURN=30s", "DAMNBOT_SERVE_TEST_STATE=" + state}, "--drain-timeout=100ms")
	awaitState(t, p, state, "turn-started", 10*time.Second)

	start := time.Now()
	p.terminate()
	code := p.waitExit(6 * time.Second) // drainCancelGrace (5 s) + 1 s
	if elapsed := time.Since(start); elapsed > 6*time.Second {
		t.Fatalf("stuck turn held shutdown for %s, want ≤ 6 s", elapsed)
	}
	if code != 0 {
		t.Fatalf("forced exit = %d, want 0\nstderr:\n%s", code, p.stderr.String())
	}
	for _, want := range []string{"shutdown backstop", "ignored cancellation", "forcing exit"} {
		if !strings.Contains(p.stderr.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, p.stderr.String())
		}
	}
	if stateExists(state, "turn-completed") {
		t.Errorf("the 30 s turn completed instead of being cut off:\n%s", p.stderr.String())
	}
}

// TestServeReadyzWriteBlocked holds the SQLite writer lock from another
// process (this test's store) and expects /readyz to turn 503 with the `db`
// reason while /healthz stays 200.
func TestServeReadyzWriteBlocked(t *testing.T) {
	e := newEnv(t)
	p := startServe(t, e, binPaths.main, nil)
	if code, body := obsGet(t, p.url("/readyz")); code != http.StatusOK {
		t.Fatalf("initial /readyz = %d %q", code, body)
	}

	release, writeDone, db := holdWriter(t, e)
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close ctxdb: %v", cerr)
		}
	}()
	releaseAt := time.AfterFunc(3*time.Second, release)
	saw503, last := waitReadyStatus(t, p, http.StatusServiceUnavailable, 2500*time.Millisecond)
	releaseAt.Stop()
	release()
	if werr := <-writeDone; werr == nil {
		t.Error("fault-injection write unexpectedly committed")
	}
	if !saw503 {
		t.Fatalf("/readyz stayed ready while the writer was blocked; last = %s", last)
	}
	if !strings.Contains(last, `"db"`) {
		t.Errorf("503 body does not name the db check: %s", last)
	}
	if code, body := obsGet(t, p.url("/healthz")); code != http.StatusOK {
		t.Errorf("/healthz = %d %q while only the writer is blocked", code, body)
	}
	if ok, last := waitReadyStatus(t, p, http.StatusOK, 5*time.Second); !ok {
		t.Fatalf("/readyz did not recover: %s", last)
	}
}

// holdWriter opens the data dir as another process and holds BEGIN IMMEDIATE
// until release is called.
func holdWriter(t *testing.T, e *Env) (release func(), done <-chan error, db *ctxdb.Store) {
	t.Helper()
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: e.DataDir, Kind: "cli"})
	if err != nil {
		t.Fatalf("open ctxdb: %v", err)
	}
	holding := make(chan struct{})
	releaseCh := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(releaseCh) }) }
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- db.Write(context.Background(), func(*ctxdb.Tx) error {
			close(holding)
			<-releaseCh
			return errors.New("readiness fault injection rollback")
		})
	}()
	<-holding
	return release, writeDone, db
}

// waitReadyStatus polls /readyz until it reports want or the limit expires.
func waitReadyStatus(t *testing.T, p *serveProc, want int, limit time.Duration) (bool, string) {
	t.Helper()
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	last := ""
	for {
		select {
		case <-deadline.C:
			return false, last
		case <-tick.C:
			code, body := obsGet(t, p.url("/readyz"))
			last = fmt.Sprintf("%d %s", code, body)
			if code == want {
				return true, last
			}
		}
	}
}

func TestServeRejectsNonLoopbackBind(t *testing.T) {
	e := newEnv(t)
	res := e.Exec("serve", "--config", e.cfg, "--data-dir", e.DataDir, "--bind", "0.0.0.0:0")
	if res.Code != 78 {
		t.Fatalf("exit = %d, want 78\nstderr:\n%s", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "loopback") {
		t.Errorf("stderr lacks the loopback refusal: %q", res.Stderr)
	}
	if _, err := os.Stat(filepath.Join(e.DataDir, "damnbot.db")); err == nil {
		t.Errorf("invalid bind still opened the database")
	}
}
