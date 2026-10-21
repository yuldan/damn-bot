package modules

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// fakeClock is a settable clock for retry-window tests.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// fakeModule records lifecycle calls; release + startedCh let a test hold
// Start open and observe that it began.
type fakeModule struct {
	startErr  error
	stopErr   error
	started   atomic.Int32
	stopped   atomic.Int32
	release   chan struct{}
	startedCh chan struct{}
	once      sync.Once
}

func (m *fakeModule) Start(context.Context) error {
	m.started.Add(1)
	m.once.Do(func() {
		if m.startedCh != nil {
			close(m.startedCh)
		}
	})
	if m.release != nil {
		<-m.release
	}
	return m.startErr
}

func (m *fakeModule) Status() host.Status { return host.StatusReady }

func (m *fakeModule) Invoke(context.Context, string, json.RawMessage, host.Env) (host.Result, error) {
	return host.Result{Content: "ok"}, nil
}

func (m *fakeModule) Stop(context.Context) error {
	m.stopped.Add(1)
	return m.stopErr
}

// eventHost records emitted module events.
type eventHost struct {
	mu     sync.Mutex
	events []host.Event
}

func (h *eventHost) Emit(_ context.Context, ev host.Event) error {
	h.mu.Lock()
	h.events = append(h.events, ev)
	h.mu.Unlock()
	return nil
}

func (h *eventHost) I18n() host.Translator { return nil }

func (h *eventHost) Spawn(context.Context, host.SpawnSpec) (host.Process, error) {
	return nil, errors.New("not implemented")
}

func (h *eventHost) LLM(string) host.Completer { return nil }

func (h *eventHost) Storage(string) (host.Store, error) {
	return nil, errors.New("not implemented")
}

func (h *eventHost) count(kind host.Event) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, ev := range h.events {
		if ev.Kind == kind.Kind {
			n++
		}
	}
	return n
}

// lifecycleFixture builds a frozen single-module runtime.
type lifecycleFixture struct {
	rt    *Runtime
	clock *fakeClock
	host  *eventHost
	mod   *fakeModule
	id    string
}

func newLifecycleFixture(t *testing.T, d Descriptor) *lifecycleFixture { //nolint:gocritic // test helper copies one descriptor
	t.Helper()
	mod := &fakeModule{}
	d.Factory = func(host.Host, host.Config) (Module, error) { return mod, nil }
	r := NewRegistry()
	if err := r.Register(d); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := r.Freeze(); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	clock := newClock()
	eh := &eventHost{}
	rt := NewRuntime(Deps{Registry: r, Host: eh, Now: clock.Now})
	return &lifecycleFixture{rt: rt, clock: clock, host: eh, mod: mod, id: d.ID}
}

func TestModuleLifecycle(t *testing.T) {
	t.Run("dormant_start", testLifecycleDormantStart)
	t.Run("unknown_and_disabled", testLifecycleUnknownAndDisabled)
	t.Run("start_failure", testLifecycleStartFailure)
	t.Run("factory_panic", testLifecycleFactoryPanic)
	t.Run("stop", testLifecycleStop)
	t.Run("concurrent", testLifecycleConcurrent)
	t.Run("idle_sweep", testLifecycleIdleSweep)
	t.Run("needs_attention", testLifecycleNeedsAttention)
	t.Run("transitions_require_active", testLifecycleRequiresActive)
	t.Run("fail", testLifecycleFail)
}

func testLifecycleDormantStart(t *testing.T) {
	f := newLifecycleFixture(t, descriptor("browser"))
	if got := f.rt.State(f.id); got != Dormant {
		t.Fatalf("state = %s, want dormant", got)
	}
	if err := f.rt.Ensure(context.Background(), f.id); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := f.rt.State(f.id); got != Active {
		t.Fatalf("state = %s, want active", got)
	}
	if got := f.rt.Status(f.id); got != host.StatusReady {
		t.Fatalf("status = %s", got)
	}
	if got := f.mod.started.Load(); got != 1 {
		t.Fatalf("Start calls = %d, want 1", got)
	}
	if err := f.rt.Ensure(context.Background(), f.id); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if got := f.mod.started.Load(); got != 1 {
		t.Fatalf("second Ensure restarted the module (%d starts)", got)
	}
	if _, ok := f.rt.Module(f.id); !ok {
		t.Fatal("active module not exposed")
	}
	if got := f.host.count(host.Event{Kind: host.EventModuleStart}); got != 1 {
		t.Fatalf("module_start events = %d, want 1", got)
	}
}

func testLifecycleUnknownAndDisabled(t *testing.T) {
	f := newLifecycleFixture(t, descriptor("browser"))
	if err := f.rt.Ensure(context.Background(), "missing"); !errors.Is(err, ErrUnknownModule) {
		t.Fatalf("unknown module error = %v", err)
	}
	off := NewRuntime(Deps{Registry: f.rt.reg, Enabled: func(string) bool { return false }})
	if err := off.Ensure(context.Background(), f.id); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled error = %v", err)
	}
}

func testLifecycleStartFailure(t *testing.T) {
	f := newLifecycleFixture(t, descriptor("browser"))
	f.mod.startErr = errors.New("backend down")
	if err := f.rt.Ensure(context.Background(), f.id); !errors.Is(err, ErrModuleFailed) {
		t.Fatalf("Ensure = %v, want ErrModuleFailed", err)
	}
	if got := f.rt.State(f.id); got != Failed {
		t.Fatalf("state = %s, want failed", got)
	}
	f.clock.Add(RetryFailed - time.Second)
	if err := f.rt.Ensure(context.Background(), f.id); !errors.Is(err, ErrModuleFailed) {
		t.Fatalf("early retry = %v, want ErrModuleFailed", err)
	}
	if got := f.mod.started.Load(); got != 1 {
		t.Fatalf("early retry started the module (%d)", got)
	}
	f.mod.startErr = nil
	f.clock.Add(2 * time.Second)
	if err := f.rt.Ensure(context.Background(), f.id); err != nil {
		t.Fatalf("retry after window: %v", err)
	}
	if got := f.rt.State(f.id); got != Active {
		t.Fatalf("state = %s, want active", got)
	}
	if got := f.mod.started.Load(); got != 2 {
		t.Fatalf("Start calls = %d, want 2", got)
	}
	if got := f.host.count(host.Event{Kind: host.EventModuleError}); got != 1 {
		t.Fatalf("module_error events = %d, want 1", got)
	}
}

func testLifecycleFactoryPanic(t *testing.T) {
	d := descriptor("browser")
	var boom func()
	d.Factory = func(host.Host, host.Config) (Module, error) {
		boom()
		return nil, nil
	}
	r := NewRegistry()
	if err := r.Register(d); err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(Deps{Registry: r, Now: newClock().Now})
	if err := rt.Ensure(context.Background(), d.ID); !errors.Is(err, ErrModuleFailed) {
		t.Fatalf("Ensure = %v, want ErrModuleFailed", err)
	}
	if got := rt.State(d.ID); got != Failed {
		t.Fatalf("state = %s, want failed", got)
	}
}

func testLifecycleStop(t *testing.T) {
	f := newLifecycleFixture(t, descriptor("browser"))
	if err := f.rt.Ensure(context.Background(), f.id); err != nil {
		t.Fatal(err)
	}
	if err := f.rt.Stop(context.Background(), f.id); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := f.rt.Stop(context.Background(), f.id); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
	if got := f.mod.stopped.Load(); got != 1 {
		t.Fatalf("Stop calls = %d, want 1", got)
	}
	if got := f.rt.State(f.id); got != Dormant {
		t.Fatalf("state = %s, want dormant", got)
	}
}

func testLifecycleConcurrent(t *testing.T) {
	f := newLifecycleFixture(t, descriptor("browser"))
	f.mod.release = make(chan struct{})
	f.mod.startedCh = make(chan struct{})
	const callers = 4
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			errs <- f.rt.Ensure(context.Background(), f.id)
		})
	}
	<-f.mod.startedCh
	close(f.mod.release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Ensure: %v", err)
		}
	}
	if got := f.mod.started.Load(); got != 1 {
		t.Fatalf("Start calls = %d, want 1", got)
	}
}

func testLifecycleIdleSweep(t *testing.T) {
	d := descriptor("browser")
	d.IdleStop = 10 * time.Minute
	f := newLifecycleFixture(t, d)
	if err := f.rt.Ensure(context.Background(), f.id); err != nil {
		t.Fatal(err)
	}
	if got := f.rt.Sweep(context.Background()); got != 0 {
		t.Fatalf("Sweep too early = %d, want 0", got)
	}
	f.clock.Add(11 * time.Minute)
	if got := f.rt.Sweep(context.Background()); got != 1 {
		t.Fatalf("Sweep = %d, want 1", got)
	}
	if got := f.rt.State(f.id); got != Dormant {
		t.Fatalf("state = %s, want dormant", got)
	}
	if err := f.rt.Ensure(context.Background(), f.id); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if got := f.mod.started.Load(); got != 2 {
		t.Fatalf("Start calls = %d, want 2", got)
	}
}

func testLifecycleNeedsAttention(t *testing.T) { //nolint:gocyclo // linear lifecycle scenario
	f := newLifecycleFixture(t, descriptor("gateway.telegram"))
	if err := f.rt.Ensure(context.Background(), f.id); err != nil {
		t.Fatal(err)
	}
	if err := f.rt.MarkNeedsAttention(context.Background(), f.id, "temporary_ban"); err != nil {
		t.Fatalf("MarkNeedsAttention: %v", err)
	}
	if got := f.rt.State(f.id); got != NeedsAttention {
		t.Fatalf("state = %s, want needs_attention", got)
	}
	if got := f.rt.Status(f.id); got != host.StatusNeedsAttention {
		t.Fatalf("status = %s", got)
	}
	if got := f.host.count(host.Event{Kind: host.EventModuleError}); got != 1 {
		t.Fatalf("module_error events = %d, want 1", got)
	}
	if got := f.mod.stopped.Load(); got != 1 {
		t.Fatalf("needs_attention did not stop the instance (%d)", got)
	}
	// No auto-retry before the 24 h temporary_ban timer.
	f.clock.Add(23 * time.Hour)
	if err := f.rt.Ensure(context.Background(), f.id); !errors.Is(err, ErrNeedsAttention) {
		t.Fatalf("Ensure before timer = %v, want ErrNeedsAttention", err)
	}
	if got := f.mod.started.Load(); got != 1 {
		t.Fatalf("auto-retried early (%d starts)", got)
	}
	// The timer allows exactly one owner-less retry.
	f.clock.Add(2 * time.Hour)
	if err := f.rt.Ensure(context.Background(), f.id); err != nil {
		t.Fatalf("Ensure after timer: %v", err)
	}
	if got := f.mod.started.Load(); got != 2 {
		t.Fatalf("timer retry starts = %d, want 2", got)
	}
	// A second needs_attention has no timer retry left.
	if err := f.rt.MarkNeedsAttention(context.Background(), f.id, "banned"); err != nil {
		t.Fatal(err)
	}
	f.clock.Add(48 * time.Hour)
	if err := f.rt.Ensure(context.Background(), f.id); !errors.Is(err, ErrNeedsAttention) {
		t.Fatalf("second episode Ensure = %v, want ErrNeedsAttention", err)
	}
	if got := f.mod.started.Load(); got != 2 {
		t.Fatalf("second episode auto-retried (%d starts)", got)
	}
	// Owner action restores the module and the allowance.
	if err := f.rt.Resolve(f.id); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := f.rt.State(f.id); got != Dormant {
		t.Fatalf("state after Resolve = %s, want dormant", got)
	}
	if err := f.rt.Ensure(context.Background(), f.id); err != nil {
		t.Fatalf("Ensure after Resolve: %v", err)
	}
	if got := f.mod.started.Load(); got != 3 {
		t.Fatalf("starts after Resolve = %d, want 3", got)
	}
}

func testLifecycleRequiresActive(t *testing.T) {
	f := newLifecycleFixture(t, descriptor("browser"))
	if err := f.rt.MarkNeedsAttention(context.Background(), f.id, "nope"); !errors.Is(err, ErrInactive) {
		t.Fatalf("error = %v, want ErrInactive", err)
	}
	if err := f.rt.Resolve(f.id); !errors.Is(err, ErrInactive) {
		t.Fatalf("Resolve error = %v, want ErrInactive", err)
	}
}

func testLifecycleFail(t *testing.T) {
	f := newLifecycleFixture(t, descriptor("browser"))
	if err := f.rt.Ensure(context.Background(), f.id); err != nil {
		t.Fatal(err)
	}
	if err := f.rt.Fail(context.Background(), f.id, errors.New("invoke panic")); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	if got := f.rt.State(f.id); got != Failed {
		t.Fatalf("state = %s, want failed", got)
	}
	if err := f.rt.Ensure(context.Background(), f.id); !errors.Is(err, ErrModuleFailed) {
		t.Fatalf("immediate retry = %v", err)
	}
	f.clock.Add(RetryFailed)
	if err := f.rt.Ensure(context.Background(), f.id); err != nil {
		t.Fatalf("retry after window: %v", err)
	}
}
