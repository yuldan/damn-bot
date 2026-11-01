package provider_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/damnbot/damnbot/internal/provider"
)

// TestGAPF5_PROV01 pins the shared carrier contract: the closed kind set and
// exact origin projection. Each adapter's own TestGAPF5_PROV01 proves the
// wire bytes for its protocol.
func TestGAPF5_PROV01(t *testing.T) {
	t.Parallel()
	kinds := []provider.CarrierKind{
		provider.CarrierThoughtSignature, provider.CarrierThinking,
		provider.CarrierRedactedThinking, provider.CarrierEncrypted, provider.CarrierOpaque,
	}
	raw := []byte(`{"type":"reasoning","signature":"SIG-A"}`)
	carrier := provider.Carrier{Family: "origin", Model: "m", Kind: provider.CarrierThinking, Raw: raw, Replayable: true}
	rt := &provider.Route{Model: "m", Family: "origin"}
	sent, drops, err := provider.ProjectCarriers(rt, []provider.Carrier{carrier})
	if err != nil || len(sent) != 1 || len(drops) != 0 || !bytes.Equal(sent[0].Raw, raw) {
		t.Fatalf("same route: sent=%+v drops=%+v err=%v", sent, drops, err)
	}
	rt.Model = "other"
	sent, drops, err = provider.ProjectCarriers(rt, []provider.Carrier{carrier})
	if err != nil || len(sent) != 0 || len(drops) != 1 || drops[0].Reason != "route_mismatch" {
		t.Fatalf("other route: sent=%+v drops=%+v err=%v", sent, drops, err)
	}
	for _, kind := range kinds {
		if !kind.Valid() {
			t.Errorf("%s is not a valid kind", kind)
		}
	}
	if _, _, err := provider.ProjectCarriers(rt, []provider.Carrier{{
		Family: "origin", Model: "m", Kind: "made-up",
		Raw: json.RawMessage(`{"a":1}`), Replayable: true,
	}}); err == nil {
		t.Fatal("unknown carrier kind accepted")
	}
}
