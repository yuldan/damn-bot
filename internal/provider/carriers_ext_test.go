package provider_test

import (
	"bytes"
	"testing"

	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

func carrierThinking() provider.Carrier {
	raw := []byte(`{"type":"thinking","thinking":"plan","signature":"sig-abc"}`)
	return provider.Carrier{Family: "anthropic", Model: "claude", Kind: provider.CarrierThinking, Anchor: "0", Raw: raw, Replayable: true}
}

func carrierRoute(baseURL string) provider.Route {
	return provider.Route{Provider: "anthropic", Model: "claude", Family: "anthropic", BaseURL: baseURL, APIKey: "k"}
}

func carrierReq(rt *provider.Route) *provider.Request {
	return &provider.Request{
		Model: rt.Ref(),
		Messages: []provider.Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Carriers: []provider.Carrier{carrierThinking()}},
		},
	}
}

func TestCarrierSignatureRetry(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, providertest.Status(400, "invalid_signature", ""), providertest.Text("ok"))
	rt := carrierRoute(srv.URL)
	o := client(t, &provider.Config{Routes: []provider.Route{rt}})
	resp, err := o.Do(t.Context(), provider.PurposeTurn, carrierReq(&rt), nil)
	if err != nil {
		t.Fatal(err)
	}
	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want exactly one retry", len(reqs))
	}
	if !bytes.Contains(reqs[0], []byte("sig-abc")) || bytes.Contains(reqs[1], []byte("sig-abc")) {
		t.Fatalf("retry did not strip the carrier (first=%s second=%s)", reqs[0], reqs[1])
	}
	if len(resp.RejectedCarriers) != 1 || resp.RejectedCarriers[0].Kind != provider.CarrierThinking {
		t.Fatalf("rejected = %+v", resp.RejectedCarriers)
	}
	if resp.Content != "ok" {
		t.Fatalf("content = %q", resp.Content)
	}
}

func TestCarrierSignatureRetryOnce(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil,
		providertest.Status(400, "invalid_signature", ""), providertest.Status(400, "invalid_signature", ""))
	rt := carrierRoute(srv.URL)
	o := client(t, &provider.Config{Routes: []provider.Route{rt}})
	if _, err := o.Do(t.Context(), provider.PurposeTurn, carrierReq(&rt), nil); err == nil {
		t.Fatal("second signature failure succeeded")
	}
	if got := len(srv.Requests()); got != 2 {
		t.Fatalf("requests = %d, want 2 (one retry only)", got)
	}
}

// TestCarrierSignatureIgnoredWithoutCarriers: a 400 invalid_signature with
// no opaque block is not retried.
func TestCarrierSignatureIgnoredWithoutCarriers(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, providertest.Status(400, "invalid_signature", ""))
	rt := carrierRoute(srv.URL)
	o := client(t, &provider.Config{Routes: []provider.Route{rt}})
	if _, err := o.Do(t.Context(), provider.PurposeTurn, &provider.Request{Model: rt.Ref()}, nil); err == nil {
		t.Fatal("bad request succeeded")
	}
	if got := len(srv.Requests()); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}
