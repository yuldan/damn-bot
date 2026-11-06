package whatsapp_web_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/outq"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/gwplat/whatsapp_web"
	"github.com/damnbot/damnbot/internal/gwwa"
)

type allowPacer struct{}

func (allowPacer) TakeChat(string, string, bool, time.Time) (bool, time.Duration) { return true, 0 }
func (allowPacer) Penalize(string, time.Time)                                     {}

type secrets map[string]string

func (s secrets) Get(_ context.Context, name string) (string, error) {
	value, ok := s[name]
	if !ok {
		return "", errors.New("missing")
	}
	return value, nil
}

const testToken = "bridge-bearer-token"

func testSecrets() secrets { return secrets{"whatsapp_web_bridge_token": testToken} }

func testOutbox(t *testing.T, d *gwplat.Descriptor) *gwplat.Outbox {
	t.Helper()
	sched, err := outq.New(&outq.Options{Pacer: allowPacer{}, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		if runErr := sched.Run(ctx); runErr != nil && !errors.Is(runErr, context.Canceled) {
			t.Errorf("scheduler: %v", runErr)
		}
	}()
	out, err := gwplat.NewOutbox(d, sched)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// fakeTransport is a scripted bridge. Hello is queued before Connect.
type fakeTransport struct {
	mu    sync.Mutex
	notif chan []byte
	round []string
	sends int
	once  sync.Once
}

func newFake(t *testing.T) *fakeTransport {
	t.Helper()
	f := &fakeTransport{notif: make(chan []byte, 16)}
	f.push(t, "bridge.hello", gwwa.Hello{
		Protocol: gwwa.Protocol, BridgeVersion: "0.1.0",
		Capabilities: gwwa.Capabilities{WaVersion: "2.3000.101", WaVersionSource: "build"},
	})
	return f
}

func (f *fakeTransport) push(t *testing.T, method string, params any) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
	if err != nil {
		t.Errorf("marshal notification: %v", err)
		return
	}
	f.notif <- raw
}

func (f *fakeTransport) RoundTrip(_ context.Context, frame []byte) ([]byte, error) {
	var req struct {
		ID     uint64 `json:"id"`
		Method string `json:"method"`
	}
	if err := json.Unmarshal(frame, &req); err != nil {
		return nil, fmt.Errorf("fake bridge decode: %w", err)
	}
	f.mu.Lock()
	f.round = append(f.round, req.Method)
	if req.Method == "send" {
		f.sends++
	}
	f.mu.Unlock()
	return []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":{}}`, req.ID)), nil
}

func (f *fakeTransport) Notifications(context.Context) (<-chan []byte, error) { return f.notif, nil }

func (f *fakeTransport) Close() error {
	f.once.Do(func() { close(f.notif) })
	return nil
}

func (f *fakeTransport) calls() (int, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sends, append([]string(nil), f.round...)
}

func descriptor(t *testing.T) gwplat.Descriptor {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	d, err := reg.Lookup("whatsapp_web")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

type harness struct {
	adapter *whatsapp_web.Adapter
	fake    *fakeTransport
	handled chan *gateway.Inbound
	notices chan string
	dials   *int
	mu      *sync.Mutex
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	d := descriptor(t)
	h := &harness{
		handled: make(chan *gateway.Inbound, 16),
		notices: make(chan string, 16),
		dials:   new(int),
		mu:      new(sync.Mutex),
	}
	h.fake = newFake(t)
	cfg := &whatsapp_web.Config{
		Enabled:      true,
		AckBanRiskAt: time.Unix(1_700_000_000, 0),
		Notify:       func(_ context.Context, notice string) { h.notices <- notice },
		Handler: gwplat.HandlerFunc(func(_ context.Context, in *gateway.Inbound) error {
			h.handled <- in
			return nil
		}),
		Backoff: gwplat.Backoff{Base: time.Millisecond, Max: time.Millisecond},
		Dial: func(context.Context) (gwwa.Transport, error) {
			h.mu.Lock()
			*h.dials++
			h.mu.Unlock()
			return h.fake, nil
		},
	}
	a, err := whatsapp_web.New(t.Context(), cfg, &d, testSecrets(), testOutbox(t, &d), cfg.Handler)
	if err != nil {
		t.Fatal(err)
	}
	h.adapter = a
	return h
}

func (h *harness) dialCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return *h.dials
}

func waitChan[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for event")
		var zero T
		return zero
	}
}

// TestF7PLAT025WABridge is the PLAT-025 acceptance: an inbound notification
// becomes one dispatch (deduped), the reply is sent to the warm JID only, and
// the bridge receives an ack. A cold JID writes nothing.
func TestF7PLAT025WABridge(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.adapter.Run(ctx) }()

	h.fake.push(t, "inbound", map[string]any{"external_id": "e1", "chat_id": "62811@s.whatsapp.net", "sender_id": "62811", "text": "hello"})
	in := waitChan(t, h.handled)
	if in.Platform != "whatsapp_web" || in.ChatID != "62811@s.whatsapp.net" || in.UpdateID != "e1" || in.Text != "hello" {
		t.Fatalf("inbound = %+v", in)
	}
	h.fake.push(t, "inbound", map[string]any{"external_id": "e1", "chat_id": "62811@s.whatsapp.net", "sender_id": "62811", "text": "hello"})
	select {
	case extra := <-h.handled:
		t.Fatalf("replay dispatched again: %+v", extra)
	case <-time.After(100 * time.Millisecond):
	}

	sends, _ := h.fake.calls()
	if sends != 0 {
		t.Fatalf("inbound wrote %d sends", sends)
	}
	if err := h.adapter.Send(t.Context(), "62811@s.whatsapp.net", "reply"); err != nil {
		t.Fatal(err)
	}
	sends, rounds := h.fake.calls()
	if sends != 1 || !contains(rounds, "ack") {
		t.Fatalf("warm reply: sends=%d rounds=%v", sends, rounds)
	}
	if err := h.adapter.Send(t.Context(), "62899@s.whatsapp.net", "cold"); !errors.Is(err, whatsapp_web.ErrColdOutbound) {
		t.Fatalf("cold reply = %v", err)
	}
	if sends, _ := h.fake.calls(); sends != 1 {
		t.Fatalf("cold reply wrote a request: %d", sends)
	}
	cancel()
	if err := waitChan(t, done); err != nil {
		t.Fatalf("run: %v", err)
	}
}

func contains(hay []string, needle string) bool {
	return slices.Contains(hay, needle)
}

// TestWhatsAppWebGuards is the GAP-GW-01 negative control: without the single
// ban-risk acknowledgement the bridge is never dialled, the caps pace one
// message per three seconds, and no secret reaches an error.
func TestWhatsAppWebGuards(t *testing.T) {
	d := descriptor(t)
	h := newHarness(t)

	t.Run("no_ack", func(t *testing.T) {
		dialed := 0
		cfg := &whatsapp_web.Config{
			Enabled: true,
			Dial: func(context.Context) (gwwa.Transport, error) {
				dialed++
				return newFake(t), nil
			},
			Handler: gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil }),
		}
		_, err := whatsapp_web.New(t.Context(), cfg, &d, testSecrets(), testOutbox(t, &d), cfg.Handler)
		if !errors.Is(err, whatsapp_web.ErrBanRiskAck) || !errors.Is(err, gwplat.ErrUnavailable) {
			t.Fatalf("unacked activation = %v", err)
		}
		if dialed != 0 {
			t.Fatal("unacked activation dialled the bridge")
		}
	})

	t.Run("disabled", func(t *testing.T) {
		cfg := &whatsapp_web.Config{AckBanRiskAt: time.Now()}
		if _, err := whatsapp_web.New(t.Context(), cfg, &d, testSecrets(), h.adapter.Outbox(), cfg.Handler); !errors.Is(err, gwplat.ErrUnavailable) {
			t.Fatalf("disabled = %v", err)
		}
	})

	t.Run("missing_secret", func(t *testing.T) {
		cfg := &whatsapp_web.Config{
			Enabled: true, AckBanRiskAt: time.Now(), Endpoint: "http://127.0.0.1:1",
			Handler: gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil }),
		}
		_, err := whatsapp_web.New(t.Context(), cfg, &d, secrets{}, h.adapter.Outbox(), cfg.Handler)
		if !errors.Is(err, gwplat.ErrMissingSecret) {
			t.Fatalf("missing secret = %v", err)
		}
		if err != nil && strings.Contains(err.Error(), testToken) {
			t.Fatalf("error leaked the token: %v", err)
		}
	})

	t.Run("pacing_caps", func(t *testing.T) {
		per := d.Caps.Rate.PerChat
		if per.PerSecond > 1.0/3.0+0.0001 || per.PerSecond < 1.0/3.0-0.0001 || per.Burst != 1 {
			t.Fatalf("per-chat bucket = %+v, want 1/3 s", per)
		}
		spacing := time.Duration(float64(time.Second) / per.PerSecond)
		if total := 99 * spacing; total < 297*time.Second {
			t.Fatalf("100 messages pace to %s, want ≥ 297 s", total)
		}
	})
}

// TestF7GAPGW11Breaker is the adapter side of GAP-GW-11: a ban/logout class
// stops after one session with one notice, temporary_ban carries 24 hours,
// and only network/unknown reconnect.
func TestF7GAPGW11Breaker(t *testing.T) {
	for _, tc := range []struct {
		reason gwwa.HealthReason
		retry  time.Duration
	}{
		{gwwa.ReasonLoggedOut, 0},
		{gwwa.ReasonBanned, 0},
		{gwwa.ReasonTemporaryBan, 24 * time.Hour},
		{gwwa.ReasonProtocolOutdated, 0},
	} {
		t.Run(string(tc.reason), func(t *testing.T) {
			h := newHarness(t)
			done := make(chan error, 1)
			go func() { done <- h.adapter.Run(t.Context()) }()
			h.fake.push(t, "health", gwwa.Health{State: gwwa.StateDisconnected, Reason: tc.reason})
			err := waitChan(t, done)
			var attention *whatsapp_web.Attention
			if !errors.As(err, &attention) || attention.Reason != tc.reason || attention.RetryAfter != tc.retry {
				t.Fatalf("%s = %v (%+v)", tc.reason, err, attention)
			}
			waitChan(t, h.notices)
			if h.dialCount() != 1 {
				t.Fatalf("%s retried %d times, want 1", tc.reason, h.dialCount())
			}
		})
	}

	t.Run("network_reconnects", func(t *testing.T) {
		h := newHarness(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- h.adapter.Run(ctx) }()
		h.fake.push(t, "health", gwwa.Health{State: gwwa.StateDisconnected, Reason: gwwa.ReasonNetwork})
		deadline := time.After(5 * time.Second)
		for h.dialCount() < 2 {
			select {
			case <-deadline:
				t.Fatalf("network disconnect did not reconnect (dials=%d)", h.dialCount())
			case <-time.After(time.Millisecond):
			}
		}
		cancel()
		if err := waitChan(t, done); err != nil {
			t.Fatalf("network run = %v", err)
		}
		select {
		case notice := <-h.notices:
			t.Fatalf("network disconnect raised needs_attention: %s", notice)
		default:
		}
	})
}
