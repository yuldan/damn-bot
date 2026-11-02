//go:build !minimal

package gemini_test

import (
	"bytes"
	"testing"

	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/gemini"
)

// TestGAPF5_PROV01 keeps Gemini thought_signature bytes on the function-call
// part and replays them only to the origin route.
func TestGAPF5_PROV01(t *testing.T) {
	t.Parallel()
	raw := `{"text":"plan","thought_signature":"SIG-G"}`
	rt := &provider.Route{Provider: "p", Model: "m", BaseURL: "https://origin.example/v1", Family: "origin"}
	req := &provider.Request{Model: "m", Messages: []provider.Message{{
		Role: "assistant", Content: "answer",
		Carriers: []provider.Carrier{{Family: "origin", Model: "m", Kind: provider.CarrierThoughtSignature, Raw: []byte(raw), Replayable: true}},
	}}}
	body, sent, drops, err := gemini.New().Encode(rt, req, nil)
	if err != nil || len(sent) != 1 || len(drops) != 0 || !bytes.Contains(body, []byte(raw)) {
		t.Fatalf("same route: body=%s sent=%+v drops=%+v err=%v", body, sent, drops, err)
	}
	rt.Model = "other"
	body, sent, drops, err = gemini.New().Encode(rt, req, nil)
	if err != nil || len(sent) != 0 || len(drops) != 1 || drops[0].Reason != "route_mismatch" || bytes.Contains(body, []byte("SIG-")) {
		t.Fatalf("other route: body=%s sent=%+v drops=%+v err=%v", body, sent, drops, err)
	}
}
