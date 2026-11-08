package e2e

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/outq"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/gwplat/line"
)

type inboundIDs struct{}

func (inboundIDs) Identity(_ context.Context, platform, externalID string) (gateway.Identity, bool, error) {
	return gateway.Identity{
		Platform: platform, ExternalID: externalID, Role: gateway.RoleOwner,
		Status: gateway.StatusAllowlisted, Strength: gateway.StrengthPlatformAuthenticated,
	}, true, nil
}

type inboxRows struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (i *inboxRows) Record(_ context.Context, in *gateway.Inbound) (bool, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	key := in.Platform + "|" + in.UpdateID
	if i.seen[key] {
		return false, nil
	}
	i.seen[key] = true
	return true, nil
}

type lineSecrets map[string]string

func (s lineSecrets) Get(_ context.Context, name string) (string, error) { return s[name], nil }

type pacer struct{}

func (pacer) TakeChat(string, string, bool, time.Time) (bool, time.Duration) { return true, 0 }
func (pacer) Penalize(string, time.Time)                                     {}

// TestF7GwplatInboundPrompt proves webhook → admission → exactly one prompt.
func TestF7GwplatInboundPrompt(t *testing.T) {
	gate, err := gateway.New(&gateway.Options{Identities: inboundIDs{}, Inbox: &inboxRows{seen: map[string]bool{}}})
	if err != nil {
		t.Fatal(err)
	}
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	desc, err := reg.Lookup("line")
	if err != nil {
		t.Fatal(err)
	}
	sched, err := outq.New(&outq.Options{Pacer: pacer{}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := gwplat.NewOutbox(&desc, sched)
	if err != nil {
		t.Fatal(err)
	}
	prompts := 0
	a, err := line.New(t.Context(), &line.Config{Enabled: true}, &desc,
		lineSecrets{"line_channel_secret": "sig", "line_access_token": "tok"}, out,
		gwplat.HandlerFunc(func(ctx context.Context, in *gateway.Inbound) error {
			_, aerr := gate.Admit(ctx, in, func(context.Context, gateway.Ticket) error {
				prompts++
				return nil
			})
			if aerr != nil {
				return fmt.Errorf("admit: %w", aerr)
			}
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	body := `{"events":[{"type":"message","replyToken":"rt","source":{"userId":"U1"},"message":{"id":"m1","type":"text","text":"halo"}}]}`
	sig := signHMAC("sig", []byte(body))
	for range 2 {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/line", strings.NewReader(body))
		req.Header.Set("X-Line-Signature", sig)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("webhook = %d", rec.Code)
		}
	}
	if prompts != 1 {
		t.Fatalf("prompts = %d, want 1", prompts)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/line", strings.NewReader(body))
	req.Header.Set("X-Line-Signature", "bad")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || prompts != 1 {
		t.Fatalf("forged status=%d prompts=%d", rec.Code, prompts)
	}
}

func signHMAC(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}
