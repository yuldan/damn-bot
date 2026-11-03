//go:build !minimal

package webtools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// TestNewFromConfigDisabled: an enabled=false section builds a stopped module
// without a client, and Invoke still fails closed with module_unavailable.
func TestNewFromConfigDisabled(t *testing.T) {
	t.Parallel()
	m, err := NewFromConfig(json.RawMessage(`{"enabled":false,"engines":["fake"]}`))
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	if serr := m.Start(context.Background()); serr != nil {
		t.Fatal(serr)
	}
	if m.Status() != host.StatusStopped {
		t.Errorf("status = %s", m.Status())
	}
	_, err = m.Invoke(context.Background(), "web_fetch", json.RawMessage(`{"url":"https://example.test"}`), host.Env{})
	wantCode(t, err, "module_unavailable")
}

// TestNewFromConfigEnabled: the full composition-root shape starts ready and
// serves the deterministic fake search backend.
func TestNewFromConfigEnabled(t *testing.T) {
	t.Parallel()
	m, err := NewFromConfig(json.RawMessage(`{"enabled":true,"engines":["fake"],"user_agent":"damnbot/test"}`))
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	if serr := m.Start(context.Background()); serr != nil {
		t.Fatal(serr)
	}
	if m.Status() != host.StatusReady {
		t.Fatalf("status = %s", m.Status())
	}
	res, err := m.Invoke(context.Background(), "web_search", json.RawMessage(`{"query":"golang","limit":1}`), host.Env{})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if res.UntrustedSource != "web_search:fake" {
		t.Errorf("taint = %q", res.UntrustedSource)
	}
}

// TestNewFromConfigRejects: the section is strict and an enabled module must
// carry the versioned User-Agent (SEC-25).
func TestNewFromConfigRejects(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"empty":    ``,
		"unknown":  `{"enabled":false,"bogus":1}`,
		"bad_json": `{`,
		"no_ua":    `{"enabled":true,"engines":["fake"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewFromConfig(json.RawMessage(raw)); !errors.Is(err, errBadConfig) {
				t.Fatalf("err = %v, want errBadConfig", err)
			}
		})
	}
}
