package obs

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
)

// OTLPConfig configures the opt-in OTLP/HTTP exporter (OPS-018). An empty
// endpoint or Enabled=false means the exporter is off and opens no socket.
type OTLPConfig struct {
	Enabled  bool
	Endpoint string
	Interval time.Duration
	MaxQueue int
	Client   *http.Client // nil = netx.LoopbackClient
	// Sample, when set, runs before each periodic flush so the exporter
	// carries the pull metrics of the process.
	Sample func()
}

const (
	defaultOTLPInterval = 30 * time.Second
	defaultOTLPQueue    = 256
	maxOTLPBody         = 1 << 20
	// maxAttrValue bounds one exported attribute; prompt content is never
	// exported at all (the key filter below drops it).
	maxAttrValue = 128
)

// ErrOTLPDisabled is returned by Flush when the exporter is off.
var ErrOTLPDisabled = errors.New("obs: otlp disabled")

// Attr is one exported attribute.
type Attr struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Sample is one metric data point.
type Sample struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
	Attrs []Attr  `json:"attrs,omitempty"`
}

// Exporter is a bounded, non-blocking OTLP/HTTP metrics exporter.
type Exporter struct {
	cfg     OTLPConfig
	client  *http.Client
	ch      chan Sample
	dropped atomic.Uint64
	started atomic.Bool
	done    chan struct{}
	wg      sync.WaitGroup
	now     func() time.Time
}

// ValidateOTLPEndpoint accepts an http(s) URL on a loopback host. The
// exporter uses netx.LoopbackClient, so a public collector would bypass the
// egress registry; local collectors are the supported shape.
func ValidateOTLPEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("telemetry.otlp_endpoint: invalid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("telemetry.otlp_endpoint: scheme must be http or https")
	}
	host := u.Hostname()
	if host != "localhost" && host != "::1" && !strings.HasPrefix(host, "127.") {
		return fmt.Errorf("telemetry.otlp_endpoint: host must be loopback")
	}
	return nil
}

// NewOTLP builds the exporter. A disabled exporter is a valid value: Record
// returns false, Start does nothing, and Flush returns ErrOTLPDisabled
// without dialing.
func NewOTLP(cfg OTLPConfig) (*Exporter, error) {
	if cfg.Interval <= 0 {
		cfg.Interval = defaultOTLPInterval
	}
	if cfg.MaxQueue <= 0 {
		cfg.MaxQueue = defaultOTLPQueue
	}
	if cfg.Enabled {
		if err := ValidateOTLPEndpoint(cfg.Endpoint); err != nil {
			return nil, err
		}
	}
	e := &Exporter{cfg: cfg, client: cfg.Client, ch: make(chan Sample, cfg.MaxQueue), done: make(chan struct{}), now: time.Now}
	if e.client == nil {
		e.client = netx.LoopbackClient()
	}
	return e, nil
}

// SetSampler installs the periodic sample callback; call it before Start.
func (e *Exporter) SetSampler(fn func()) {
	if e != nil {
		e.cfg.Sample = fn
	}
}

// Enabled reports whether records will be exported.
func (e *Exporter) Enabled() bool { return e != nil && e.cfg.Enabled }

// Record queues one sample without blocking. It returns false when the
// exporter is disabled or the bounded queue is full; a full queue increments
// the drop counter exported as damnbot_otlp_dropped_total.
func (e *Exporter) Record(name string, value float64, attrs ...Attr) bool {
	if !e.Enabled() {
		return false
	}
	s := Sample{Name: name, Value: value, Attrs: sanitizeAttrs(attrs)}
	select {
	case e.ch <- s:
		return true
	default:
		e.dropped.Add(1)
		return false
	}
}

// Dropped is the number of samples rejected by the full queue.
func (e *Exporter) Dropped() uint64 {
	if e == nil {
		return 0
	}
	return e.dropped.Load()
}

func sanitizeAttrs(attrs []Attr) []Attr {
	out := make([]Attr, 0, len(attrs))
	for _, a := range attrs {
		key := strings.TrimSpace(a.Key)
		if key == "" || sensitiveKey(key) {
			continue
		}
		value := a.Value
		if len(value) > maxAttrValue {
			value = value[:maxAttrValue]
		}
		out = append(out, Attr{Key: key, Value: value})
	}
	return out
}

func sensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	for _, s := range []string{"prompt", "content", "message", "token", "secret", "password", "api_key", "authorization", "cookie"} {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

// Start launches the periodic flush loop; it is a no-op when disabled or
// already started.
func (e *Exporter) Start(ctx context.Context) {
	if !e.Enabled() || !e.started.CompareAndSwap(false, true) {
		return
	}
	e.wg.Go(func() {
		t := time.NewTicker(e.cfg.Interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-e.done:
				return
			case <-t.C:
				if e.cfg.Sample != nil {
					e.cfg.Sample()
				}
				if err := e.Flush(ctx); err != nil {
					if ctx.Err() != nil {
						return
					}
					continue
				}
			}
		}
	})
}

// Shutdown stops the loop and flushes once; it is safe to call twice.
func (e *Exporter) Shutdown(ctx context.Context) error {
	if !e.Enabled() {
		return nil
	}
	select {
	case <-e.done:
	default:
		close(e.done)
	}
	e.wg.Wait()
	return e.Flush(ctx)
}

// Flush drains the queue and posts one OTLP ExportMetricsServiceRequest.
func (e *Exporter) Flush(ctx context.Context) error {
	if !e.Enabled() {
		return ErrOTLPDisabled
	}
	samples := e.drain()
	samples = append(samples, Sample{Name: "damnbot_otlp_dropped_total", Value: float64(e.dropped.Load())})
	body := encodeMetrics(samples, e.now())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, metricsURL(e.cfg.Endpoint), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("obs: otlp request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	resp, err := e.client.Do(req)
	if err != nil {
		return fmt.Errorf("obs: otlp post: %w", err)
	}
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxOTLPBody)); err != nil {
		return errors.Join(fmt.Errorf("obs: otlp response: %w", err), resp.Body.Close())
	}
	if err := resp.Body.Close(); err != nil {
		return fmt.Errorf("obs: close response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("obs: otlp status %d", resp.StatusCode)
	}
	return nil
}

func (e *Exporter) drain() []Sample {
	var out []Sample
	for {
		select {
		case s := <-e.ch:
			out = append(out, s)
		default:
			return out
		}
	}
}

func metricsURL(endpoint string) string {
	return strings.TrimRight(endpoint, "/") + "/v1/metrics"
}

// Export records the current pull metrics as OTLP samples; serve calls it on
// the exporter interval so /metrics and OTLP carry the same numbers.
func (m *Metrics) Export(e *Exporter) {
	if e == nil {
		return
	}
	e.Record("damnbot_uptime_seconds", time.Since(m.start).Seconds())
	e.Record("damnbot_turns_total", float64(m.ok.Load()), Attr{Key: "result", Value: "ok"})
	e.Record("damnbot_turns_total", float64(m.fail.Load()), Attr{Key: "result", Value: "error"})
	e.Record("damnbot_errors_total", float64(m.errs.Load()))
	e.Record("damnbot_queue_depth", float64(m.queue.Load()))
	e.Record("damnbot_lease_held", boolFloat(m.lease.Load() == 1))
}

func boolFloat(v bool) float64 {
	if v {
		return 1
	}
	return 0
}

// encodeMetrics builds an OTLP ExportMetricsServiceRequest with one gauge
// per sample. The protobuf subset is hand-written so the module stays free of
// a new direct dependency (SPEC P21).
func encodeMetrics(samples []Sample, now time.Time) []byte {
	var scope []byte
	for _, s := range samples {
		scope = append(scope, encodeMetric(s, now)...)
	}
	scopeMsg := pbMessage(2, scope) // ScopeMetrics.metrics
	resource := pbMessage(1, slices.Concat(pbString("service.name"), pbMessage(2, pbString("damnbot"))))
	scopeHead := pbMessage(1, pbString("damnbot"))
	rm := slices.Concat(pbMessage(1, resource), pbMessage(2, slices.Concat(scopeHead, scopeMsg)))
	return pbMessage(1, rm)
}

func encodeMetric(s Sample, now time.Time) []byte {
	var point []byte
	point = append(point, pbFixed64(2, uint64(now.UnixNano()))...) // start = now
	point = append(point, pbFixed64(3, uint64(now.UnixNano()))...)
	point = append(point, pbFixed64(4, math.Float64bits(s.Value))...)
	for _, a := range s.Attrs {
		kv := slices.Concat(pbString(a.Key), pbMessage(2, pbString(a.Value)))
		point = append(point, pbMessage(7, kv)...)
	}
	gauge := pbMessage(1, point)
	return slices.Concat(pbMessage(1, pbString(s.Name)), pbMessage(5, gauge))
}

func pbMessage(field int, body []byte) []byte {
	out := pbVarint(uint64(field)<<3 | 2) // #nosec G115 -- protobuf field numbers are positive constants
	out = append(out, pbVarint(uint64(len(body)))...)
	return append(out, body...)
}

func pbString(s string) []byte {
	return pbMessage(1, []byte(s))
}

func pbVarint(v uint64) []byte {
	var buf [10]byte
	n := binary.PutUvarint(buf[:], v)
	return buf[:n]
}

func pbFixed64(field int, v uint64) []byte {
	out := pbVarint(uint64(field)<<3 | 1) // #nosec G115 -- protobuf field numbers are positive constants
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)
	return append(out, buf[:]...)
}
