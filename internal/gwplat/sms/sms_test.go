package sms

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/outq"
	"github.com/damnbot/damnbot/internal/gwplat"
)

type secrets map[string]string

func (s secrets) Get(_ context.Context, name string) (string, error) {
	v, ok := s[name]
	if !ok {
		return "", errors.New("missing")
	}
	return v, nil
}

type allowPacer struct{}

func (allowPacer) TakeChat(string, string, bool, time.Time) (bool, time.Duration) { return true, 0 }
func (allowPacer) Penalize(string, time.Time)                                     {}

type cost struct {
	segments int
	micros   int64
	hit      chan struct{}
}

func (c *cost) RecordChannelCost(_ context.Context, _, _ string, segments int, micros int64) error {
	c.segments += segments
	c.micros += micros
	if c.hit != nil {
		select {
		case c.hit <- struct{}{}:
		default:
		}
	}
	return nil
}

func newAdapter(t *testing.T, api string, cfg *Config, in gwplat.Handler) (*Adapter, *gwplat.Outbox) {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	desc, err := reg.Lookup("sms")
	if err != nil {
		t.Fatal(err)
	}
	sched, err := outq.New(&outq.Options{Pacer: allowPacer{}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := gwplat.NewOutbox(&desc, sched)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		if rerr := sched.Run(ctx); rerr != nil {
			return
		}
	}()
	if cfg == nil {
		cfg = &Config{}
	}
	cfg.Enabled, cfg.API = true, api
	a, err := New(ctx, cfg, &desc, secrets{"sms_account_sid": "AC1", "sms_auth_token": "auth-token", "sms_from": "+1000"}, out, in)
	if err != nil {
		t.Fatal(err)
	}
	return a, out
}

func TestF7GAPGW09Segments(t *testing.T) {
	cases := []struct {
		text string
		seg  int
		enc  string
	}{
		{"hello", 1, SMSEncodingGSM7},
		{strings.Repeat("a", 160), 1, SMSEncodingGSM7},
		{strings.Repeat("a", 161), 2, SMSEncodingGSM7},
		{strings.Repeat("a", 307), 3, SMSEncodingGSM7},
		{"€", 1, SMSEncodingGSM7},
		{"😀", 1, SMSEncodingUCS2},
		{strings.Repeat("😀", 36), 2, SMSEncodingUCS2},
	}
	for _, c := range cases {
		if got, enc := Segments(c.text); got != c.seg || enc != c.enc {
			t.Errorf("Segments(%d) = %d/%s, want %d/%s", len(c.text), got, enc, c.seg, c.enc)
		}
	}
	body, n, _, truncated := FitSMS(strings.Repeat("a", 10000), SMSMaxSegments, "\n"+SMSMoreHint)
	if !truncated || n > 3 || !strings.HasSuffix(body, SMSMoreHint) {
		t.Fatalf("10000 chars = %d segments truncated=%v", n, truncated)
	}
}

func TestF7PLAT020SMS(t *testing.T) {
	forms := make(chan url.Values, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "AC1" || p != "auth-token" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		forms <- r.PostForm
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	var got []gateway.Inbound
	spend := &cost{}
	a, _ := newAdapter(t, srv.URL, &Config{Cost: spend, CostPerSegmentMicros: 1000}, gwplat.HandlerFunc(func(_ context.Context, in *gateway.Inbound) error {
		got = append(got, *in)
		return nil
	}))
	h := a.Handler()
	form := url.Values{"MessageSid": {"SM1"}, "From": {"+1555"}, "To": {"+1666"}, "Body": {"halo"}}
	raw := form.Encode()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://example.com/sms", strings.NewReader(raw))
	sig := twilioSignature("auth-token", gwplat.ExternalURL(req), form)
	for range 2 {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://example.com/sms", strings.NewReader(raw))
		req.Header.Set("X-Twilio-Signature", sig)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("valid = %d", rec.Code)
		}
	}
	if len(got) != 1 {
		t.Fatalf("turns = %d", len(got))
	}
	bad := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://example.com/sms", strings.NewReader(raw))
	bad.Header.Set("X-Twilio-Signature", "bad")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bad)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad signature = %d", rec.Code)
	}
	sendSMS(t, a, forms, spend)
}

func sendSMS(t *testing.T, a *Adapter, forms chan url.Values, spend *cost) {
	if err := a.Send(t.Context(), "+1555", strings.Repeat("a", 10000)); err != nil {
		t.Fatal(err)
	}
	body := awaitValue(t, forms)
	if body.Get("To") != "+1555" || body.Get("From") != "+1000" {
		t.Fatalf("form = %v", body)
	}
	if !strings.HasSuffix(strings.TrimSpace(body.Get("Body")), SMSMoreHint) {
		t.Fatalf("body tail = %q", body.Get("Body"))
	}
	if spend.segments < 2 || spend.segments > 3 || spend.micros != int64(spend.segments)*1000 {
		t.Fatalf("ledger = %+v", spend)
	}
}

func TestF7SMSLedger(t *testing.T) {
	// One reply is one billed message: segments never exceed the cap and the
	// ledger records price × segments exactly once.
	spend := &cost{hit: make(chan struct{}, 1)}
	a, _ := newAdapter(t, "http://127.0.0.1:1", &Config{Cost: spend, CostPerSegmentMicros: 500}, gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil }))
	if err := a.Send(t.Context(), "+1555", strings.Repeat("a", 460)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-spend.hit:
	case <-time.After(5 * time.Second):
		t.Fatal("no ledger record")
	}
	if spend.segments != 3 || spend.micros != 1500 {
		t.Fatalf("ledger = %+v", spend)
	}
}

var _ = sync.Mutex{}

func awaitValue[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("no value")
		var zero T
		return zero
	}
}
