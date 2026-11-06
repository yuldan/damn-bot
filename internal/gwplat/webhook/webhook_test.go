package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/outq"
	"github.com/damnbot/damnbot/internal/gwplat"
)

type allowPacer struct{}

func (allowPacer) TakeChat(string, string, bool, time.Time) (bool, time.Duration) { return true, 0 }
func (allowPacer) Penalize(string, time.Time)                                     {}

type secretMap map[string]string

func (s secretMap) Get(_ context.Context, name string) (string, error) {
	value, ok := s[name]
	if !ok || value == "" {
		return "", errors.New("secret not found")
	}
	return value, nil
}

func testDescriptor(t *testing.T, name string) *gwplat.Descriptor {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	d, err := reg.Lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	return &d
}

func testOutbox(t *testing.T, name string) *gwplat.Outbox {
	t.Helper()
	sched, err := outq.New(&outq.Options{Pacer: allowPacer{}, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		if runErr := sched.Run(ctx); runErr != nil && !errors.Is(runErr, context.Canceled) {
			t.Errorf("scheduler: %v", runErr)
		}
	}()
	t.Cleanup(func() {
		cancel()
		sched.Close()
	})
	out, err := gwplat.NewOutbox(testDescriptor(t, name), sched)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

type recorder struct {
	mu  sync.Mutex
	ins []gateway.Inbound
	err error
}

func (r *recorder) Handle(_ context.Context, in *gateway.Inbound) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.ins = append(r.ins, *in)
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.ins)
}

func testAdapter(t *testing.T, h gwplat.Handler, secret string) *Adapter {
	t.Helper()
	a, err := New(t.Context(), &Config{Enabled: true}, testDescriptor(t, "webhook"),
		secretMap{"webhook_secret": secret}, testOutbox(t, "webhook"), h)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func testEnvelope(t *testing.T, id, text string) []byte {
	t.Helper()
	body, err := json.Marshal(Envelope{
		ID: id, Event: "message.created", Chat: "ops", Sender: "alice", Text: text,
	})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func signed(secret string, body []byte) http.Header {
	header := http.Header{}
	header.Set(SignatureHeader, Sign([]byte(secret), body))
	return header
}

// TestF7PLAT029Webhook (PLAT-029): the generic receiver admits one signed
// envelope, maps it to the gateway inbound fields, answers the HTTP surface,
// and refuses an oversized body before dispatch.
func TestF7PLAT029Webhook(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	a := testAdapter(t, rec, "s3cr3t")
	body := testEnvelope(t, "evt-1", "deploy finished")
	n, err := a.Handle(t.Context(), signed("s3cr3t", body), body)
	if err != nil || n != 1 {
		t.Fatalf("Handle = %d, %v", n, err)
	}
	in := rec.ins[0]
	if in.Platform != "webhook" || in.ChatID != "ops" || in.SenderID != "alice" ||
		in.Text != "deploy finished" || in.UpdateID != "webhook:evt-1" {
		t.Fatalf("inbound = %+v", in)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/hook", bytes.NewReader(body))
	req.Header = signed("s3cr3t", body)
	a.ServeHTTP(w, req)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "ok" {
		t.Fatalf("ServeHTTP = %d %q", w.Code, w.Body.String())
	}
	get := httptest.NewRecorder()
	a.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/hook", nil))
	if get.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405", get.Code)
	}
	big := make([]byte, MaxBodyBytes+1)
	tooBig := httptest.NewRecorder()
	a.ServeHTTP(tooBig, httptest.NewRequest(http.MethodPost, "/hook", bytes.NewReader(big)))
	if tooBig.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body = %d, want 413", tooBig.Code)
	}
	if rec.count() != 1 {
		t.Fatalf("dispatches = %d, want 1", rec.count())
	}
}

// TestF7WebhookSignature (EC-SEC-006 negative controls): a wrong or tampered
// signature runs 0 turns, and activation without a secret leaves the endpoint
// inactive.
func TestF7WebhookSignature(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	a := testAdapter(t, rec, "s3cr3t")
	body := testEnvelope(t, "evt-sig", "hello")
	if n, err := a.Handle(t.Context(), signed("wrong", body), body); n != 0 || !errors.Is(err, ErrSignature) {
		t.Fatalf("wrong signature = %d, %v", n, err)
	}
	swapped := bytes.Replace(body, []byte("hello"), []byte("pwned"), 1)
	if n, err := a.Handle(t.Context(), signed("s3cr3t", body), swapped); n != 0 || !errors.Is(err, ErrSignature) {
		t.Fatalf("tampered body = %d, %v", n, err)
	}
	noHeader := http.Header{}
	if n, err := a.Handle(t.Context(), noHeader, body); n != 0 || !errors.Is(err, ErrSignature) {
		t.Fatalf("missing header = %d, %v", n, err)
	}
	if rec.count() != 0 {
		t.Fatalf("dispatches = %d, want 0", rec.count())
	}

	if _, err := New(t.Context(), &Config{Enabled: true}, testDescriptor(t, "webhook"), nil,
		testOutbox(t, "webhook"), rec); !errors.Is(err, gwplat.ErrMissingSecret) {
		t.Fatalf("missing secret = %v, want ErrMissingSecret", err)
	}
	if _, err := New(t.Context(), &Config{}, testDescriptor(t, "webhook"), secretMap{"webhook_secret": "x"},
		testOutbox(t, "webhook"), rec); !errors.Is(err, gwplat.ErrUnavailable) {
		t.Fatalf("disabled receiver = %v, want ErrUnavailable", err)
	}
}

// TestF7WebhookSignatureNeverLogsPayload: even at debug level the adapter never
// writes the message text or the body into a log record.
func TestF7WebhookSignatureNeverLogsPayload(t *testing.T) {
	t.Parallel()
	var logs bytes.Buffer
	rec := &recorder{}
	a, err := New(t.Context(), &Config{
		Enabled: true,
		Log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})).
			With("component", "webhook"),
	},
		testDescriptor(t, "webhook"), secretMap{"webhook_secret": "s3cr3t"}, testOutbox(t, "webhook"), rec)
	if err != nil {
		t.Fatal(err)
	}
	body := testEnvelope(t, "evt-log", "top-secret-payload")
	if _, herr := a.Handle(t.Context(), signed("s3cr3t", body), body); herr != nil {
		t.Fatal(herr)
	}
	if strings.Contains(logs.String(), "top-secret-payload") {
		t.Fatalf("payload reached the log: %s", logs.String())
	}
}

// TestF7WebhookDedupe (PLAT-029): a retried envelope id becomes one turn, both
// on the direct path and through the HTTP surface.
func TestF7WebhookDedupe(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	a := testAdapter(t, rec, "s3cr3t")
	body := testEnvelope(t, "evt-dup", "once")
	for range 2 {
		if n, err := a.Handle(t.Context(), signed("s3cr3t", body), body); err != nil || n > 1 {
			t.Fatalf("Handle = %d, %v", n, err)
		}
	}
	if rec.count() != 1 {
		t.Fatalf("replayed id dispatches = %d, want 1", rec.count())
	}
	for range 2 {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/hook", bytes.NewReader(body))
		req.Header = signed("s3cr3t", body)
		a.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("ServeHTTP replay = %d", w.Code)
		}
	}
	if rec.count() != 1 {
		t.Fatalf("HTTP replay dispatches = %d, want 1", rec.count())
	}
	other := testEnvelope(t, "evt-other", "twice")
	if n, err := a.Handle(t.Context(), signed("s3cr3t", other), other); err != nil || n != 1 {
		t.Fatalf("distinct id = %d, %v", n, err)
	}
	if rec.count() != 2 {
		t.Fatalf("dispatch total = %d, want 2", rec.count())
	}
}
