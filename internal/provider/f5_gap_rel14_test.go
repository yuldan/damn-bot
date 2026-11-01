package provider_test

import (
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/netx/retry"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestGAPF5_REL14 covers the F5-owned known-defect scenarios that can be
// proven offline: an oversized-payload 413 is surfaced explicitly instead of
// being silently truncated, and a cut stream is retried/typed rather than
// accepted as a final answer.
func TestGAPF5_REL14(t *testing.T) {
	t.Parallel()
	t.Run("payload-too-large-413", func(t *testing.T) {
		t.Parallel()
		srv := providertest.New(t, nil, providertest.Status(413, "payload_too_large", ""))
		o := client(t, &provider.Config{Routes: []provider.Route{route("m", srv.URL)}})
		resp, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
		var pe *provider.Error
		if !errors.As(err, &pe) || pe.HTTPStatus != 413 || pe.Class != retry.BadRequest {
			t.Fatalf("413 = %+v, %v; want typed 413 bad_request", resp, err)
		}
		if resp.Content != "" || len(srv.Requests()) != 1 {
			t.Fatalf("413 accepted as answer (content=%q, requests=%d)", resp.Content, len(srv.Requests()))
		}
	})
	t.Run("cut-stream-is-not-an-answer", func(t *testing.T) {
		t.Parallel()
		cut := providertest.Reply{Frames: []string{`{"choices":[{"delta":{"content":"partial"}}]}`}}
		srv := providertest.New(t, nil, cut, cut, cut)
		o := client(t, &provider.Config{Routes: []provider.Route{route("m", srv.URL)}})
		resp, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil)
		if retry.ClassifyErr(err) != retry.StreamBroken {
			t.Fatalf("cut stream = %+v, %v; want stream_broken", resp, err)
		}
		if resp.Content == "partial" {
			t.Fatal("cut stream accepted as the final answer")
		}
		if got := len(srv.Requests()); got != retry.MaxAttempts {
			t.Fatalf("requests = %d, want %d retries", got, retry.MaxAttempts)
		}
	})
}
