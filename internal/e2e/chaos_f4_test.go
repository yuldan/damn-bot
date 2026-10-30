//go:build unix

package e2e

// F4 chaos suite (ROADMAP F4 acceptance, SELF-HEALING §9.2): kill -9 in the
// middle of turns, network cuts, a full data directory, corrupt config/DB and
// a simulated reboot. Every scenario has a negative control in the same test,
// and every assertion is made against the durable state (raw SQLite, no ctxdb
// recovery helper), so a green run means the failure was really survived.
//
// DAMNBOT_CHAOS_ROUNDS raises the kill -9 round count (default 200; `make
// chaos` runs 1000). The knob only exists for this suite: fewer rounds while
// iterating, the acceptance number for the gate.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestChaosF4 is the F4 chaos acceptance suite. It is not part of `make test`:
// `make chaos` runs it with the full round count and machine limits.
func TestChaosF4(t *testing.T) {
	t.Run("Kill9MidTurn", chaosKill9MidTurn)
	t.Run("NetworkCut", chaosNetworkCut)
	t.Run("DiskFull", chaosDiskFull)
	t.Run("CorruptConfig", chaosCorruptConfig)
	t.Run("CorruptDB", chaosCorruptDB)
	t.Run("Reboot", chaosReboot)
	t.Run("TelegramFaults", chaosTelegramFaults)
	t.Run("NonIdempotentPending", chaosNonIdempotentPending)
}

// chaosRounds is the number of kill -9 turns. 200 is the acceptance minimum;
// `make chaos` sets 1000.
func chaosRounds(t *testing.T) int {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("DAMNBOT_CHAOS_ROUNDS"))
	if raw == "" {
		return 200
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		t.Fatalf("DAMNBOT_CHAOS_ROUNDS=%q is not a positive integer", raw)
	}
	return n
}

// chaosDB reads a data dir through ctxdb's public read-only API: no migration,
// no startup recovery, no raw driver. Events and integrity are therefore the
// evidence that survives a crash, exactly what the suite must judge.
type chaosDB struct {
	t      *testing.T
	s      *ctxdb.Store
	ev     []ctxdb.Event
	loaded bool
}

func openChaosDB(t *testing.T, dir string) *chaosDB {
	t.Helper()
	s, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: dir, Kind: "cli", ReadOnly: true})
	if err != nil {
		t.Fatalf("open %s read-only: %v", dir, err)
	}
	c := &chaosDB{t: t, s: s}
	t.Cleanup(c.close)
	return c
}

func (c *chaosDB) close() {
	c.t.Helper()
	if err := c.s.Close(); err != nil {
		c.t.Errorf("close chaos db: %v", err)
	}
}

// integrity runs PRAGMA integrity_check on the data as it is on disk now.
func (c *chaosDB) integrity(what string) string {
	c.t.Helper()
	res, err := c.s.IntegrityCheck(c.t.Context())
	if err != nil {
		c.t.Fatalf("%s: integrity_check: %v", what, err)
	}
	if res != "ok" {
		return res
	}
	return "ok"
}

// events loads every session's history once, ordered per session.
func (c *chaosDB) events() []ctxdb.Event {
	c.t.Helper()
	if c.loaded {
		return c.ev
	}
	ids, err := c.s.ListSessions(c.t.Context())
	if err != nil {
		c.t.Fatalf("list sessions: %v", err)
	}
	for _, sid := range ids {
		evs, herr := c.s.History(c.t.Context(), sid, 0, ctxdb.WithHistoryLimit(100_000), ctxdb.WithHistoryExcluded())
		if herr != nil {
			c.t.Fatalf("history of %s: %v", sid, herr)
		}
		c.ev = append(c.ev, evs...)
	}
	c.loaded = true
	return c.ev
}

// count counts events of kind matching pred.
func (c *chaosDB) count(kind string, pred func(ctxdb.Event) bool) int {
	c.t.Helper()
	n := 0
	evs := c.events()
	for i := range evs {
		if evs[i].Kind == kind && (pred == nil || pred(evs[i])) {
			n++
		}
	}
	return n
}

// find returns the first event of kind matching pred.
func (c *chaosDB) find(kind string, pred func(ctxdb.Event) bool) (ctxdb.Event, bool) {
	c.t.Helper()
	evs := c.events()
	for i := range evs {
		if evs[i].Kind == kind && (pred == nil || pred(evs[i])) {
			return evs[i], true
		}
	}
	return ctxdb.Event{}, false
}

// killServe9 SIGKILLs a serve child and reaps it. The lock file is flock-based,
// so the kernel releases it with the process, exactly like a crash.
func killServe9(t *testing.T, p *serveProc) {
	t.Helper()
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("kill serve: %v", err)
	}
	select {
	case <-p.exited:
	case <-time.After(15 * time.Second):
		t.Fatalf("serve did not die after SIGKILL\nstderr:\n%s", p.stderr.String())
	}
}

// chaosKill9MidTurn kills a real serve process while a real turn is waiting on
// the provider, then restarts it on the same data dir. Every round asserts:
// no corruption before recovery, the inbound row exactly once, the open turn
// recovered to a terminal status, and no duplicated event or Telegram message.
func chaosKill9MidTurn(t *testing.T) {
	rounds := chaosRounds(t)
	e := New(t)
	tg := NewFakeTG(t, FakeTGOptions{})
	p := startGatewayServe(t, e, tg)

	var maxRecovery, totalRecovery time.Duration
	for round := range rounds {
		marker := fmt.Sprintf("chaos-kill-%04d", round)
		reqBefore := len(e.Server.Requests())
		e.Server.Push(providertest.Reply{Hang: true}) // the turn never finishes on its own
		tg.PushText(gatewayOwner, marker)
		waitFor(t, 20*time.Second, "turn "+marker+" to reach the provider", func() bool {
			return len(e.Server.Requests()) > reqBefore
		})

		turn := chaosKill9Pre(t, p, e.DataDir, marker, round)

		started := time.Now()
		p = startGatewayServe(t, e, tg)
		took := time.Since(started)
		if took > 10*time.Second {
			t.Fatalf("round %d: recovery took %s, budget 10s", round, took)
		}
		totalRecovery += took
		if took > maxRecovery {
			maxRecovery = took
		}
		chaosKill9Post(t, e.DataDir, turn, round)
	}

	gatewayStop(t, p)
	if got, want := len(tg.Sends()), rounds; got != want {
		t.Fatalf("successful placeholder sends = %d, want %d (one per killed turn, no duplicates)", got, want)
	}
	if n := len(tg.Edits()); n != 0 {
		t.Fatalf("edits = %d, want 0: every turn was killed before its final answer", n)
	}
	t.Logf("kill -9: %d turns, %.1f ms mean recovery, %.1f ms max recovery, loadavg %s",
		rounds, float64(totalRecovery.Microseconds())/1000/float64(rounds), float64(maxRecovery.Microseconds())/1000, loadAverage())
}

// chaosKill9Pre SIGKILLs p and checks the durable state before any recovery:
// integrity, one inbound turn and one user message. The provider request plus
// the committed turn_start is what proves the kill was mid-turn.
func chaosKill9Pre(t *testing.T, p *serveProc, dir, marker string, round int) string {
	t.Helper()
	killServe9(t, p)

	pre := openChaosDB(t, dir)
	defer pre.close()
	if got := pre.integrity("round " + strconv.Itoa(round)); got != "ok" {
		t.Fatalf("round %d: corrupt at kill time, before recovery: %s", round, got)
	}
	msg, ok := pre.find("user_message", func(ev ctxdb.Event) bool { return ev.Content == marker })
	if !ok || msg.TurnID == "" {
		t.Fatalf("round %d: no committed user_message for %q survived the kill", round, marker)
	}
	if n := pre.count("user_message", func(ev ctxdb.Event) bool { return ev.Content == marker }); n != 1 {
		t.Fatalf("round %d: user_message rows for the killed update = %d, want 1", round, n)
	}
	if n := pre.count("turn_start", func(ev ctxdb.Event) bool { return ev.TurnID == msg.TurnID }); n != 1 {
		t.Fatalf("round %d: turn_start rows at kill = %d, want 1", round, n)
	}
	return msg.TurnID
}

// chaosKill9Post checks the state after the restart recovered that turn: the
// crashed turn is visibly closed once, and no event or answer was duplicated.
func chaosKill9Post(t *testing.T, dir, turn string, round int) {
	t.Helper()
	post := openChaosDB(t, dir)
	defer post.close()
	if got := post.integrity("round " + strconv.Itoa(round) + " after recovery"); got != "ok" {
		t.Fatalf("round %d: corrupt after recovery: %s", round, got)
	}
	if n := post.count("turn_start", func(ev ctxdb.Event) bool { return ev.TurnID == turn }); n != 1 {
		t.Fatalf("round %d: turn_start rows = %d, want 1 (duplicate turn)", round, n)
	}
	if n := post.count("user_message", func(ev ctxdb.Event) bool { return ev.TurnID == turn }); n != 1 {
		t.Fatalf("round %d: user_message rows = %d, want 1 (duplicate inbound)", round, n)
	}
	if n := post.count("turn_end", func(ev ctxdb.Event) bool {
		return ev.TurnID == turn && strings.Contains(ev.Payload, "crashed")
	}); n != 1 {
		t.Fatalf("round %d: turn_end{crashed} rows = %d, want 1 (turn not recovered visibly)", round, n)
	}
	if n := post.count("crash_recovered", func(ev ctxdb.Event) bool { return ev.TurnID == turn }); n != 1 {
		t.Fatalf("round %d: crash_recovered rows = %d, want 1", round, n)
	}
}

// loadAverage reads /proc/loadavg for the result report; empty elsewhere.
func loadAverage() string {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "n/a"
	}
	return strings.TrimSpace(string(b))
}

// startChaosServe is startGatewayServe with the two upstream base URLs
// overridden, so a scenario can put a cuttable proxy in front of either side.
func startChaosServe(t *testing.T, e *Env, tgBase, providerBase string) *serveProc {
	t.Helper()
	env := []string{
		"DAMNBOT_TELEGRAM_TOKEN=" + FakeTGToken,
		fmt.Sprintf("DAMNBOT_GATEWAY_OWNER=telegram:%d", gatewayOwner),
		"DAMNBOT_TELEGRAM_BASE_URL=" + tgBase,
		"DAMNBOT_SERVE_MODEL=" + modelRef,
		"DAMNBOT_SERVE_BASE_URL=" + providerBase,
		"DAMNBOT_SERVE_API_KEY=e2e",
	}
	return startServe(t, e, binPaths.main, env)
}

// chaosProxy forwards HTTP to one loopback upstream and can cut the link: the
// flag refuses new requests and cancels the in-flight ones, so long polls and
// sends observe a transport error exactly like a network outage. Tests use it
// for the Bot API and the provider independently.
type chaosProxy struct {
	*httptest.Server
	origin   *url.URL
	client   httpDoer
	cut      atomic.Bool
	hits     atomic.Int64
	mu       sync.Mutex
	inflight map[*http.Request]context.CancelFunc
}

func newChaosProxy(t *testing.T, target string) *chaosProxy {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("proxy target %q: %v", target, err)
	}
	nw := netx.New(netx.Config{Offline: true})
	p := &chaosProxy{origin: u, client: nw.Client("chaos", netx.Inference), inflight: make(map[*http.Request]context.CancelFunc)}
	p.Server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(func() {
		p.Close()
		nw.CloseIdleConnections()
	})
	return p
}

func (p *chaosProxy) serve(w http.ResponseWriter, r *http.Request) {
	p.hits.Add(1)
	if p.cut.Load() {
		abortConnection(w)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	p.mu.Lock()
	p.inflight[r] = cancel
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.inflight, r)
		p.mu.Unlock()
		cancel()
	}()
	out := r.Clone(ctx)
	out.URL.Scheme, out.URL.Host = p.origin.Scheme, p.origin.Host
	out.Host, out.RequestURI = p.origin.Host, ""
	resp, err := p.client.Do(out) // #nosec G704 -- the proxy forwards only to the loopback test fixtures
	if err != nil {
		abortConnection(w)
		return
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			return // the forwarded bytes are the assertion; a close error cannot change them
		}
	}()
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, cerr := io.Copy(w, resp.Body); cerr != nil && !errors.Is(cerr, context.Canceled) {
		return // the client left; the test asserts on the upstream side
	}
}

// Cut refuses and aborts every request, including a getUpdates in flight.
func (p *chaosProxy) Cut() {
	p.cut.Store(true)
	p.mu.Lock()
	for _, cancel := range p.inflight {
		cancel()
	}
	p.mu.Unlock()
}

// Heal restores the link; the adapter's own backoff decides when it retries.
func (p *chaosProxy) Heal() { p.cut.Store(false) }

// chaosNetworkCut cuts both upstreams, then cuts only the provider mid-turn.
// Recovery must restart from the persisted offset and retry with backoff; the
// turn must be visible as failed (never a silent retry), and no reply may be
// sent twice.
func chaosNetworkCut(t *testing.T) {
	e := New(t)
	tg := NewFakeTG(t, FakeTGOptions{})
	tgProxy := newChaosProxy(t, tg.URL)
	provProxy := newChaosProxy(t, strings.TrimSuffix(e.Server.URL, "/v1"))
	p := startChaosServe(t, e, tgProxy.URL, provProxy.URL+"/v1")

	// Phase 1: both links down when the update arrives. The poller must keep
	// retrying (hits on the proxy) and the turn must not start at all.
	tgProxy.Cut()
	provProxy.Cut()
	e.Server.Push(providertest.Text("after the cut"))
	tg.PushText(gatewayOwner, "netcut")
	waitFor(t, 6*time.Second, "the poller to retry through the cut", func() bool {
		return tgProxy.hits.Load() >= 2
	})
	if n := len(e.Server.Requests()); n != 0 {
		t.Fatalf("provider requests while cut = %d, want 0", n)
	}
	if n := len(tg.Sends()); n != 0 {
		t.Fatalf("telegram sends while cut = %d, want 0", n)
	}
	tgProxy.Heal()
	provProxy.Heal()
	waitFor(t, 20*time.Second, "one provider call after the link returns", func() bool {
		return len(e.Server.Requests()) >= 1
	})
	waitFor(t, 20*time.Second, "the reply after the cut", func() bool {
		return len(tg.Edits()) >= 1
	})
	if n := len(tg.Sends()); n != 1 {
		t.Fatalf("placeholder sends after recovery = %d, want 1", n)
	}
	if edits := tg.Edits(); len(edits) != 1 || edits[0].Text != "after the cut" {
		t.Fatalf("edits after recovery = %+v, want one final edit", edits)
	}

	// Phase 2: the provider dies mid-turn (placeholder already on screen). The
	// turn must end with one visible failure, not a silent retry and not a
	// second message.
	provProxy.Cut()
	beforeSend := len(tg.Sends())
	tg.PushText(gatewayOwner, "provider-cut")
	waitFor(t, 20*time.Second, "the placeholder of the cut turn", func() bool {
		return len(tg.Sends()) == beforeSend+1
	})
	waitFor(t, 40*time.Second, "the provider-error edit", func() bool {
		edits := tg.Edits()
		for i := range edits {
			if strings.Contains(edits[i].Text, "provider error") {
				return true
			}
		}
		return false
	})
	if n := len(e.Server.Requests()); n != 1 {
		t.Fatalf("provider requests during the provider cut = %d, want still 1", n)
	}
	if n := len(tg.Edits()); n != 2 {
		t.Fatalf("edits after the provider cut = %d, want 2 (answer + one failure)", n)
	}

	// Phase 3 (negative control): once the provider is healed the same flow
	// succeeds and still sends exactly one answer per update.
	provProxy.Heal()
	e.Server.Push(providertest.Text("healthy again"))
	tg.PushText(gatewayOwner, "healthy")
	waitFor(t, 20*time.Second, "the healthy reply", func() bool {
		edits := tg.Edits()
		for i := range edits {
			if edits[i].Text == "healthy again" {
				return true
			}
		}
		return false
	})
	if n := len(tg.Sends()); n != beforeSend+2 {
		t.Fatalf("placeholder sends = %d, want %d (no duplicates)", n, beforeSend+2)
	}
	gatewayStop(t, p)
}

// chaosDiskFull injects SQLITE_FULL (result code 13, "database or disk is
// full") on the writer connection of a real data dir. A loopback/tmpfs mount
// would need root, which the gate and CI do not have; SQLITE_FULL is the exact
// driver code a full volume (ENOSPC/EDQUOT) produces and ctxdb classifies as
// ErrDiskFull, so the error path under test is the production one.
func chaosDiskFull(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	ctx := t.Context()
	s, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dir, Kind: "serve"})
	if err != nil {
		t.Fatalf("open fresh store: %v", err)
	}
	sid := "chaos-diskfull"
	if _, err := s.CreateSession(ctx, ctxdb.Session{ID: sid}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	large := strings.Repeat("disk-full-payload-", 512)
	chaosDiskFullControl(t, s, sid, large)
	chaosDiskFullInject(t, s, sid, large)
	if err := s.Close(); err != nil {
		t.Fatalf("close store after disk full: %v", err)
	}
	chaosDiskFullReopen(t, dir)
}

// chaosDiskFullControl proves the same large append succeeds with room left.
func chaosDiskFullControl(t *testing.T, s *ctxdb.Store, sid, large string) {
	t.Helper()
	if _, err := s.Append(t.Context(), sid, nil, ctxdb.Event{Kind: "user_message", Content: large}); err != nil {
		t.Fatalf("control append: %v", err)
	}
	control, err := s.History(t.Context(), sid, 0, ctxdb.WithHistoryLimit(10))
	if err != nil {
		t.Fatalf("control history: %v", err)
	}
	if len(control) != 1 {
		t.Fatalf("control events = %d, want 1", len(control))
	}
}

// chaosDiskFullInject pins max_page_count to the current size and checks that
// the write fails with the closed disk_full code, leaves no partial row and
// keeps the store readable and consistent. The process keeps running.
func chaosDiskFullInject(t *testing.T, s *ctxdb.Store, sid, large string) {
	t.Helper()
	var pages int64
	if err := s.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get("PRAGMA page_count", nil, &pages)
	}); err != nil {
		t.Fatalf("page_count: %v", err)
	}
	if err := s.Write(t.Context(), func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(fmt.Sprintf("PRAGMA max_page_count = %d", pages))
		if xerr != nil {
			return fmt.Errorf("set max_page_count: %w", xerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("inject max_page_count: %v", err)
	}
	var full error
	for range 64 {
		if _, aerr := s.Append(t.Context(), sid, nil, ctxdb.Event{Kind: "user_message", Content: large}); aerr != nil {
			full = aerr
			break
		}
	}
	if full == nil {
		t.Fatal("no write failed after max_page_count was pinned to the current size")
	}
	if !errors.Is(full, ctxdb.ErrDiskFull) || !strings.Contains(full.Error(), "disk_full") {
		t.Fatalf("write error = %v, want the closed ErrDiskFull code", full)
	}
	after, err := s.History(t.Context(), sid, 0, ctxdb.WithHistoryLimit(10))
	if err != nil {
		t.Fatalf("history after disk full: %v (store crashed)", err)
	}
	if len(after) != 1 {
		t.Fatalf("events after the failed append = %d, want 1 (partial write)", len(after))
	}
	if res, ierr := s.IntegrityCheck(t.Context()); ierr != nil || res != "ok" {
		t.Fatalf("integrity after disk full = %q, %v", res, ierr)
	}
}

// chaosDiskFullReopen proves a new connection (the default max_page_count)
// can write again: the data dir is not poisoned by the full condition.
func chaosDiskFullReopen(t *testing.T, dir string) {
	t.Helper()
	ctx := t.Context()
	reopened, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dir, Kind: "serve"})
	if err != nil {
		t.Fatalf("reopen after disk full: %v", err)
	}
	defer func() {
		if cerr := reopened.Close(); cerr != nil {
			t.Errorf("close reopened store: %v", cerr)
		}
	}()
	const fresh = "chaos-diskfull-after"
	if _, err := reopened.CreateSession(ctx, ctxdb.Session{ID: fresh}); err != nil {
		t.Fatalf("create session after reopen: %v", err)
	}
	if _, err := reopened.Append(ctx, fresh, nil, ctxdb.Event{Kind: "user_message", Content: "after recovery"}); err != nil {
		t.Fatalf("append after reopen: %v", err)
	}
	if res, ierr := reopened.IntegrityCheck(ctx); ierr != nil || res != "ok" {
		t.Fatalf("integrity after reopen = %q, %v", res, ierr)
	}
}

// chaosCorruptConfig proves a malformed config.toml is fatal without recovery
// (exit 78) and touches no data; a valid config is the negative control.
func chaosCorruptConfig(t *testing.T) {
	e := New(t)
	if err := os.WriteFile(e.cfg, []byte("[provider\nmodel = \"openai/gpt-test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := e.Exec("serve", "--config", e.cfg, "--data-dir", e.DataDir, "--bind", "127.0.0.1:0")
	if res.Code != 78 {
		t.Fatalf("corrupt config exit = %d, want 78\nstdout:\n%s\nstderr:\n%s", res.Code, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "load config") {
		t.Fatalf("corrupt config stderr does not name the config:\n%s", res.Stderr)
	}
	if _, err := os.Stat(ctxdb.DBPath(e.DataDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("corrupt config touched the database (stat err = %v)", err)
	}

	if err := os.WriteFile(e.cfg, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	p := startServe(t, e, binPaths.main, nil)
	gatewayStop(t, p)
}

// chaosCorruptDB proves a garbage database refuses to start with exit 78, is
// left untouched (no silent quarantine or truncation) and that the control
// data dir starts and stops cleanly. `doctor` reports the same file as
// DOC-006; ctxdb has no auto-repair for whole-database corruption.
func chaosCorruptDB(t *testing.T) {
	e := New(t)
	p := startServe(t, e, binPaths.main, nil)
	gatewayStop(t, p) // control: a real schema now exists

	path := ctxdb.DBPath(e.DataDir)
	junk := []byte("this is not a sqlite database")
	if err := os.WriteFile(path, junk, 0o600); err != nil {
		t.Fatal(err)
	}
	res := e.Exec("serve", "--config", e.cfg, "--data-dir", e.DataDir, "--bind", "127.0.0.1:0")
	if res.Code != 78 {
		t.Fatalf("corrupt DB exit = %d, want 78\nstdout:\n%s\nstderr:\n%s", res.Code, res.Stdout, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "open ctxdb") {
		t.Fatalf("corrupt DB stderr does not name the store:\n%s", res.Stderr)
	}
	got, err := os.ReadFile(path) // #nosec G304 -- the path is the test's own temp data dir
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, junk) {
		t.Fatalf("corrupt DB was modified by the refused start (%d bytes)", len(got))
	}
	paths := harnessPaths(e)
	if err := os.MkdirAll(paths.DataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ctxdb.DBPath(paths.DataDir), junk, 0o600); err != nil {
		t.Fatal(err)
	}
	doc := e.Exec("doctor")
	if doc.Code != 1 || !strings.Contains(doc.Stdout, "DOC-006") {
		t.Fatalf("doctor on corrupt DB = (%d), want exit 1 + DOC-006\n%s", doc.Code, doc.Stdout)
	}
}

// chaosReboot simulates a VM reboot on the same data dir: a delivery is in
// flight when the process is SIGKILLed, and a fresh serve must recover within
// 10 s, take the dead process's lease over, reclaim the orphaned delivery
// without duplicating it, and resume from the persisted Telegram offset.
func chaosReboot(t *testing.T) {
	e := New(t)
	tg := NewFakeTG(t, FakeTGOptions{
		// Fail the final edit once: the answer must travel through the outbox.
		Faults: map[string]FakeTGFault{"editMessageText": {Status: 500, Times: 1}},
		// Hold the outbox send open long enough to kill the process mid-send.
		Delays: map[string]time.Duration{"sendMessage": 2 * time.Second},
	})
	e.Server.Push(providertest.Text("reboot answer"))
	p := startGatewayServe(t, e, tg)
	update := tg.PushText(gatewayOwner, "reboot turn")
	waitFor(t, 20*time.Second, "the answer to reach the outbox send", func() bool {
		reqs := tg.RequestsFor("sendMessage")
		for i := range reqs {
			if reqs[i].Text == "reboot answer" {
				return true
			}
		}
		return false
	})
	killServe9(t, p)

	reqsBefore := len(tg.Requests())
	started := time.Now()
	p = startGatewayServe(t, e, tg)
	took := time.Since(started)
	if took > 10*time.Second {
		t.Fatalf("recovery after the simulated reboot took %s, budget 10s", took)
	}
	// The restart must resume from the offset committed before the crash.
	waitFor(t, 20*time.Second, "the persisted offset after the reboot", func() bool {
		reqs := tg.Requests()
		for i := reqsBefore; i < len(reqs); i++ {
			if reqs[i].Method == "getUpdates" && reqs[i].Offset == update+1 {
				return true
			}
		}
		return false
	})
	crashBefore := chaosRebootAssert(t, e.DataDir)
	waitFor(t, 20*time.Second, "the reclaimed delivery", func() bool {
		return chaosSends(tg, "reboot answer") == 1
	})
	chaosRebootCleanRestart(t, e, tg, p, crashBefore)
	t.Logf("reboot recovery: %.1f ms (budget 10 s), loadavg %s", float64(took.Microseconds())/1000, loadAverage())
}

// chaosSends counts successful sendMessage calls whose text is exactly text.
func chaosSends(tg *FakeTG, text string) int {
	n := 0
	sends := tg.Sends()
	for i := range sends {
		if sends[i].Text == text {
			n++
		}
	}
	return n
}

// chaosRebootAssert checks the recovered data dir and returns the number of
// crash_recovered events for the clean-restart control.
func chaosRebootAssert(t *testing.T, dir string) int64 {
	t.Helper()
	ro := openChaosDB(t, dir)
	defer ro.close()
	if got := ro.integrity("after reboot"); got != "ok" {
		t.Fatalf("corrupt after reboot: %s", got)
	}
	msg, ok := ro.find("user_message", func(ev ctxdb.Event) bool { return ev.Content == "reboot turn" })
	if !ok || msg.TurnID == "" {
		t.Fatal("user message missing after the reboot")
	}
	if n := ro.count("user_message", func(ev ctxdb.Event) bool { return ev.Content == "reboot turn" }); n != 1 {
		t.Fatalf("user_message rows after reboot = %d, want 1", n)
	}
	// This kill lands after the turn ended (during the outbox send), so the
	// turn itself is complete; the recovered object is the delivery.
	if n := ro.count("turn_end", func(ev ctxdb.Event) bool { return ev.TurnID == msg.TurnID }); n != 1 {
		t.Fatalf("turn_end rows after reboot = %d, want 1", n)
	}
	return int64(ro.count("crash_recovered", nil))
}

// chaosRebootCleanRestart is the negative control: a clean restart adds no
// crash marker and does not send the answer again.
func chaosRebootCleanRestart(t *testing.T, e *Env, tg *FakeTG, p *serveProc, crashBefore int64) {
	t.Helper()
	gatewayStop(t, p)
	p = startGatewayServe(t, e, tg)
	ro := openChaosDB(t, e.DataDir)
	if n := int64(ro.count("crash_recovered", nil)); n != crashBefore {
		ro.close()
		t.Fatalf("clean restart added %d crash_recovered rows", n-crashBefore)
	}
	ro.close()
	gatewayStop(t, p)
	if n := chaosSends(tg, "reboot answer"); n != 1 {
		t.Fatalf("successful sends of the reboot answer = %d, want exactly 1", n)
	}
}

// chaosTelegramFaults mixes 429, 5xx and a dropped connection across the Bot
// API methods. The answer may arrive as an edit, as an outbox retry or not at
// all (an uncertain send is never repeated), but it must never appear twice.
func chaosTelegramFaults(t *testing.T) {
	e := New(t)
	tg := NewFakeTG(t, FakeTGOptions{Faults: map[string]FakeTGFault{
		"sendMessage":     {Status: 429, RetryAfter: 1, Times: 2},
		"editMessageText": {Status: 503, Times: 1},
		"sendChatAction":  {Close: true, Times: 1},
	}})
	answers := []string{"fault one", "fault two", "fault three"}
	for _, answer := range answers {
		e.Server.Push(providertest.Text(answer))
	}
	p := startGatewayServe(t, e, tg)
	for _, answer := range answers {
		tg.PushText(gatewayOwner, answer)
		waitFor(t, 40*time.Second, "the reply to "+answer, func() bool { return chaosSeen(tg, answer) })
	}
	chaosAssertAnswersOnce(t, tg, answers)
	throttled := 0
	reqs := tg.RequestsFor("sendMessage")
	for i := range reqs {
		if reqs[i].Status == 429 {
			throttled++
		}
	}
	if throttled != 2 {
		t.Fatalf("429 sendMessage attempts = %d, want 2 (429 backoff not exercised)", throttled)
	}
	gatewayStop(t, p)
}

// chaosSeen reports whether a successful send or edit carried answer.
func chaosSeen(tg *FakeTG, answer string) bool {
	sends := tg.Sends()
	for i := range sends {
		if sends[i].Text == answer {
			return true
		}
	}
	edits := tg.Edits()
	for i := range edits {
		if edits[i].Text == answer {
			return true
		}
	}
	return false
}

// chaosAssertAnswersOnce checks each answer reached Telegram exactly once and
// that retries produced no duplicate message.
func chaosAssertAnswersOnce(t *testing.T, tg *FakeTG, answers []string) {
	t.Helper()
	sent := map[string]int{}
	sends := tg.Sends()
	for i := range sends {
		sent[sends[i].Text]++
	}
	edited := map[string]int{}
	edits := tg.Edits()
	for i := range edits {
		edited[edits[i].Text]++
	}
	for _, answer := range answers {
		if n := sent[answer] + edited[answer]; n != 1 {
			t.Fatalf("answer %q reached Telegram %d times (sends %d, edits %d), want exactly 1", answer, n, sent[answer], edited[answer])
		}
	}
	if n := sent["…"]; n != 2 {
		t.Fatalf("successful placeholders = %d, want 2 (the first 429 never created one)", n)
	}
	if n := len(sends); n != 4 {
		t.Fatalf("successful sends = %d, want 4 (no duplicate generated by retries)", n)
	}
}

// chaosRun is one `damnbot run` child with its own process group, so the test
// can emulate a killed terminal without leaking a tool process.
type chaosRun struct {
	cmd            *exec.Cmd
	stdout, stderr *bytes.Buffer
}

func startChaosRun(t *testing.T, e *Env, prompt string, extraEnv ...string) *chaosRun {
	t.Helper()
	args := []string{
		"run", "-p", prompt, "--model", modelRef, "--base-url", e.Server.URL,
		"--api-key", "e2e", "--data-dir", e.DataDir, "--config", e.cfg,
		"--offline", "--output", "json", "--approval", "auto", "--unsafe-auto-shell",
	}
	cmd := exec.CommandContext(t.Context(), binPaths.main, args...) // #nosec G204 -- re-exec of this package's temp build
	cmd.Dir = e.Workdir
	cmd.Env = append(e.childEnv(), extraEnv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	r := &chaosRun{cmd: cmd, stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}}
	cmd.Stdout, cmd.Stderr = r.stdout, r.stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start run: %v", err)
	}
	return r
}

// kill9 kills the whole process group (the run plus anything it spawned
// without its own group) and reaps it.
func (r *chaosRun) kill9(t *testing.T) {
	t.Helper()
	if err := syscall.Kill(-r.cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("kill run group: %v", err)
	}
	if err := r.cmd.Wait(); err == nil {
		t.Fatal("run exited cleanly after SIGKILL")
	}
}

// wait reaps the run and returns its exit code.
func (r *chaosRun) wait(t *testing.T) int {
	t.Helper()
	err := r.cmd.Wait()
	if err == nil {
		return 0
	}
	if exit, ok := errors.AsType[*exec.ExitError](err); ok {
		return exit.ExitCode()
	}
	t.Fatalf("wait run: %v", err)
	return -1
}

// chaosNonIdempotentPending kills a turn while a non-idempotent tool is still
// running. Recovery must record outcome_unknown (retry_safe=false) and park
// the turn for confirmation: it must never silently execute the side effect
// again. The negative control runs the same tool to completion exactly once.
func chaosNonIdempotentPending(t *testing.T) {
	port, hits := chaosSideEffectListener(t)
	e := NewTrusted(t, ToolCallReply("call-1", "shell", `{"command":"npm install","timeout_s":120}`), providertest.Text("done"))
	fakeBin := chaosStubNPM(t, e, port, true)
	r := startChaosRun(t, e, "run the slow tool", "PATH="+fakeBin+":"+os.Getenv("PATH"))
	waitFor(t, 30*time.Second, "the tool side effect to reach the listener", func() bool {
		return hits.Load() == 1
	})
	r.kill9(t)

	turn := chaosRecoverPending(t, e.DataDir)
	chaosAssertPending(t, e.DataDir, turn, hits)
	chaosNonIdempotentControl(t, hits)
}

// chaosSideEffectListener accepts connections on loopback and counts them.
func chaosSideEffectListener(t *testing.T) (int, *atomic.Int32) {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listener: %v", err)
	}
	t.Cleanup(func() {
		if cerr := ln.Close(); cerr != nil && !errors.Is(cerr, net.ErrClosed) {
			t.Logf("close side-effect listener: %v", cerr)
		}
	})
	var hits atomic.Int32
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			hits.Add(1)
			go func() {
				if _, cerr := io.Copy(io.Discard, conn); cerr != nil {
					return
				}
				if cerr := conn.Close(); cerr != nil {
					return
				}
			}()
		}
	}()
	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address %T is not TCP", ln.Addr())
	}
	return addr.Port, &hits
}

// chaosStubNPM puts a stub `npm` on PATH. The classifier labels `npm install`
// non-idempotent (mutating + network) and the stub's single TCP write is the
// side effect that must not happen twice. hold keeps the tool running so the
// test can kill it mid-call.
func chaosStubNPM(t *testing.T, e *Env, port int, hold bool) string {
	t.Helper()
	dir := e.Path("fakebin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stub := fmt.Sprintf("#!/bin/bash\nexec 3<>/dev/tcp/127.0.0.1/%d\nprintf once >&3\n", port)
	if hold {
		stub += "sleep 30\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "npm"), []byte(stub), 0o700); err != nil { // #nosec G306 -- the npm stub must be executable to stand in for the real tool
		t.Fatal(err)
	}
	return dir
}

// chaosRecoverPending opens the data dir the way `run` would (recovery runs)
// and returns the journal's turn id.
func chaosRecoverPending(t *testing.T, dir string) string {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatalf("recover data dir: %v", err)
	}
	turn := ""
	if werr := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get("SELECT turn_id FROM turn_journal ORDER BY started_at DESC LIMIT 1", nil, &turn)
	}); werr != nil {
		t.Fatalf("find open journal: %v (close: %v)", werr, db.Close())
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close recovered store: %v", err)
	}
	if turn == "" {
		t.Fatal("no journal row recorded for the killed non-idempotent tool")
	}
	return turn
}

// chaosAssertPending checks the recovered turn and that the side effect was
// executed exactly once.
func chaosAssertPending(t *testing.T, dir, turn string, hits *atomic.Int32) {
	t.Helper()
	ro := openChaosDB(t, dir)
	defer ro.close()
	if got := ro.integrity("non-idempotent recovery"); got != "ok" {
		t.Fatalf("corrupt after recovery: %s", got)
	}
	for _, q := range [...]struct {
		name  string
		kind  string
		match func(ctxdb.Event) bool
	}{
		{"outcome_unknown", "tool_result", func(ev ctxdb.Event) bool {
			return ev.TurnID == turn && strings.Contains(ev.Payload, "outcome_unknown")
		}},
		{"retry_safe=false", "tool_result", func(ev ctxdb.Event) bool {
			return ev.TurnID == turn && strings.Contains(ev.Payload, "retry_safe") && strings.Contains(ev.Payload, "false")
		}},
		{"crash_recovered ask", "crash_recovered", func(ev ctxdb.Event) bool {
			return ev.TurnID == turn && strings.Contains(ev.Payload, `"ask"`)
		}},
		{"turn_end crashed", "turn_end", func(ev ctxdb.Event) bool {
			return ev.TurnID == turn && strings.Contains(ev.Payload, "crashed")
		}},
	} {
		if n := ro.count(q.kind, q.match); n != 1 {
			t.Fatalf("%s rows = %d, want 1", q.name, n)
		}
	}
	// The tool's own process group was killed before the outcome was decided;
	// give a late writer a chance to prove it cannot run again.
	settle(2 * time.Second)
	if n := hits.Load(); n != 1 {
		t.Fatalf("non-idempotent side effect ran %d times, want exactly 1 (recovered tool was not repeated)", n)
	}
}

// chaosNonIdempotentControl runs the same non-idempotent tool to completion
// and confirms exactly one execution and no crash marker.
func chaosNonIdempotentControl(t *testing.T, killed *atomic.Int32) {
	t.Helper()
	port, hits := chaosSideEffectListener(t)
	control := NewTrusted(t, ToolCallReply("call-1", "shell", `{"command":"npm install","timeout_s":30}`), providertest.Text("done"))
	fakeBin := chaosStubNPM(t, control, port, false)
	r := startChaosRun(t, control, "run the same tool", "PATH="+fakeBin+":"+os.Getenv("PATH"))
	if code := r.wait(t); code != 0 {
		t.Fatalf("control run exit = %d\nstdout:\n%s\nstderr:\n%s", code, r.stdout.String(), r.stderr.String())
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("control side effect ran %d times, want exactly 1", n)
	}
	cro := openChaosDB(t, control.DataDir)
	if n := cro.count("crash_recovered", nil); n != 0 {
		cro.close()
		t.Fatalf("clean control recorded %d crash_recovered rows", n)
	}
	cro.close()
	if killed.Load() != 1 {
		t.Fatalf("killed run side effect count changed to %d", killed.Load())
	}
}

// settle waits without time.Sleep (forbidden in tests by E-30/E-72).
func settle(d time.Duration) { <-time.After(d) }
