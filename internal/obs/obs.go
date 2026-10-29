package obs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultListen is the SPEC §15 `obs.listen` default.
const DefaultListen = "127.0.0.1:7879"

const (
	healthTTL    = 3 * time.Second
	readyTimeout = time.Second
	maxReason    = 240
)

// Check is one readiness probe; Fn gets ReadyTimeout and must honour it.
type Check struct {
	Name string
	Fn   func(context.Context) error
}

// Config configures the endpoint set. HealthTTL/ReadyTimeout are test seams
// for the 3 s and 1 s SPEC defaults.
type Config struct {
	Checks       []Check
	Metrics      *Metrics
	HealthTTL    time.Duration
	ReadyTimeout time.Duration
}

// Metrics is the pull-only exposition of one serve process (GAP-REL-17).
// Fields are atomic because handlers read while subsystems write.
type Metrics struct {
	start time.Time
	ok    atomic.Uint64
	fail  atomic.Uint64
	errs  atomic.Uint64
	queue atomic.Int64
	lease atomic.Int64
	sum   atomic.Uint64 // float bits
	count atomic.Uint64
}

// NewMetrics starts the uptime clock.
func NewMetrics() *Metrics { return &Metrics{start: time.Now()} }

// Turn records one completed turn and its wall-clock latency.
func (m *Metrics) Turn(d time.Duration, ok bool) {
	if ok {
		m.ok.Add(1)
	} else {
		m.fail.Add(1)
	}
	for {
		old := m.sum.Load()
		if m.sum.CompareAndSwap(old, math.Float64bits(math.Float64frombits(old)+d.Seconds())) {
			break
		}
	}
	m.count.Add(1)
}

// IncError records one operator-visible error outside a turn.
func (m *Metrics) IncError() { m.errs.Add(1) }

// SetQueue publishes the accepted-but-not-started depth.
func (m *Metrics) SetQueue(n int) { m.queue.Store(int64(n)) }

// SetLeaseHeld publishes whether this process holds the single-serve lease.
func (m *Metrics) SetLeaseHeld(held bool) {
	if held {
		m.lease.Store(1)
		return
	}
	m.lease.Store(0)
}

// Render writes Prometheus text 0.0.4; every family is a constant number of
// identity-free series, far below the 1.000-series cap of SPEC §15.
func (m *Metrics) Render(w io.Writer) error {
	var b strings.Builder
	sample := func(name, labels, value string) {
		b.WriteString(name)
		if labels != "" {
			b.WriteString("{" + labels + "}")
		}
		b.WriteString(" " + value + "\n")
	}
	fl := func(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }
	u := func(v uint64) string { return strconv.FormatUint(v, 10) }
	b.WriteString("# TYPE damnbot_uptime_seconds gauge\n")
	sample("damnbot_uptime_seconds", "", fl(time.Since(m.start).Seconds()))
	b.WriteString("# TYPE damnbot_turns_total counter\n")
	sample("damnbot_turns_total", `result="ok"`, u(m.ok.Load()))
	sample("damnbot_turns_total", `result="error"`, u(m.fail.Load()))
	b.WriteString("# TYPE damnbot_errors_total counter\n")
	sample("damnbot_errors_total", "", u(m.errs.Load()))
	b.WriteString("# TYPE damnbot_turn_latency_seconds summary\n")
	sample("damnbot_turn_latency_seconds_sum", "", fl(math.Float64frombits(m.sum.Load())))
	sample("damnbot_turn_latency_seconds_count", "", u(m.count.Load()))
	b.WriteString("# TYPE damnbot_queue_depth gauge\n")
	sample("damnbot_queue_depth", "", strconv.FormatInt(m.queue.Load(), 10))
	b.WriteString("# TYPE damnbot_lease_held gauge\n")
	sample("damnbot_lease_held", "", strconv.FormatInt(m.lease.Load(), 10))
	b.WriteString("# TYPE damnbot_goroutines gauge\n")
	sample("damnbot_goroutines", "", strconv.Itoa(runtime.NumGoroutine()))
	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("obs: write metrics: %w", err)
	}
	return nil
}

// Endpoints is the HTTP handler of the observability listener.
type Endpoints struct {
	cfg  Config
	m    *Metrics
	beat atomic.Int64
}

// ValidateListen accepts a literal loopback IP with a port; a name or any
// other interface is refused before a socket exists.
func ValidateListen(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("obs: listen %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("obs: listen %q is not loopback; use 127.0.0.1:<port> or [::1]:<port>", addr)
	}
	if n, perr := strconv.Atoi(port); perr != nil || n < 0 || n > 65535 {
		return fmt.Errorf("obs: listen %q has an invalid port", addr)
	}
	return nil
}

// New builds the endpoint set and starts its watchdog timestamp.
func New(cfg Config) *Endpoints {
	if cfg.HealthTTL <= 0 {
		cfg.HealthTTL = healthTTL
	}
	if cfg.ReadyTimeout <= 0 {
		cfg.ReadyTimeout = readyTimeout
	}
	if cfg.Metrics == nil {
		cfg.Metrics = NewMetrics()
	}
	e := &Endpoints{cfg: cfg, m: cfg.Metrics}
	e.Beat()
	return e
}

// Metrics returns the registry this endpoint set renders.
func (e *Endpoints) Metrics() *Metrics { return e.m }

// Beat refreshes the liveness timestamp; a stalled watchdog loop fails
// `/healthz` within HealthTTL.
func (e *Endpoints) Beat() { e.beat.Store(time.Now().UnixNano()) }

// ServeHTTP routes the three GET endpoints; everything else is 404/405.
func (e *Endpoints) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case "/healthz":
		e.health(w)
	case "/readyz":
		e.ready(w, r)
	case "/metrics":
		e.metrics(w)
	default:
		http.NotFound(w, r)
	}
}

func (e *Endpoints) health(w http.ResponseWriter) {
	age := time.Since(time.Unix(0, e.beat.Load()))
	if age > e.cfg.HealthTTL {
		http.Error(w, "stale: no watchdog beat for "+age.Round(time.Millisecond).String(), http.StatusServiceUnavailable)
		return
	}
	http.Error(w, "ok", http.StatusOK)
}

func (e *Endpoints) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), e.cfg.ReadyTimeout)
	defer cancel()
	bad := map[string]string{}
	for _, c := range e.cfg.Checks {
		if c.Fn != nil {
			if err := c.Fn(ctx); err != nil {
				bad[c.Name] = oneLine(err.Error())
			}
		}
	}
	status, report := http.StatusOK, map[string]any{"status": "ok"}
	if len(bad) > 0 {
		status, report = http.StatusServiceUnavailable, map[string]any{"status": "unready", "checks": bad}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(report); err != nil {
		return
	}
}

func (e *Endpoints) metrics(w http.ResponseWriter) {
	var b strings.Builder
	if err := e.m.Render(&b); err != nil {
		http.Error(w, "metrics unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if _, err := io.WriteString(w, b.String()); err != nil {
		return
	}
}

// StartBeatLoop refreshes the liveness timestamp and publishes lease each
// interval until stop is called. The daemon owns the loop, not a hidden
// per-subsystem heartbeat (SELF-HEALING §2).
func (e *Endpoints) StartBeatLoop(ctx context.Context, interval time.Duration, lease func() bool) (stop func()) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				e.Beat()
				e.m.SetLeaseHeld(lease())
			}
		}
	}()
	return cancel
}

// Serve runs hs on ln in the background; the channel reports a non-close
// error and closes after the listener stops.
func Serve(hs *http.Server, ln net.Listener) <-chan error {
	errs := make(chan error, 1)
	go func() {
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
			errs <- fmt.Errorf("obs: serve: %w", err)
		}
		close(errs)
	}()
	return errs
}

// oneLine makes a probe error safe for one JSON field.
func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, " "))
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxReason {
		s = s[:maxReason]
	}
	return s
}
