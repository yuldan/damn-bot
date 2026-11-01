//go:build !minimal

package anthropic_test

import (
	"bytes"
	"testing"

	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/anthropic"
)

// TestGAPF5_PROV01 proves the Anthropic thinking/redacted_thinking carrier
// bytes are replayed verbatim only to the origin route.
func TestGAPF5_PROV01(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		kind provider.CarrierKind
		raw  string
	}{
		{"thinking", provider.CarrierThinking, `{"type":"thinking","thinking":"plan","signature":"SIG-A"}`},
		{"redacted", provider.CarrierRedactedThinking, `{"type":"redacted_thinking","data":"SIG-B"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rt := &provider.Route{Provider: "p", Model: "m", BaseURL: "https://origin.example/v1", Family: "origin"}
			req := &provider.Request{Model: "m", Messages: []provider.Message{{
				Role: "assistant", Content: "answer",
				Carriers: []provider.Carrier{{Family: "origin", Model: "m", Kind: tc.kind, Raw: []byte(tc.raw), Replayable: true}},
			}}}
			body, sent, drops, err := anthropic.New().Encode(rt, req, nil)
			if err != nil || len(sent) != 1 || len(drops) != 0 || !bytes.Contains(body, []byte(tc.raw)) {
				t.Fatalf("same route: body=%s sent=%+v drops=%+v err=%v", body, sent, drops, err)
			}
			rt.Model = "other"
			body, sent, drops, err = anthropic.New().Encode(rt, req, nil)
			if err != nil || len(sent) != 0 || len(drops) != 1 || drops[0].Reason != "route_mismatch" || bytes.Contains(body, []byte("SIG-")) {
				t.Fatalf("other route: body=%s sent=%+v drops=%+v err=%v", body, sent, drops, err)
			}
		})
	}
}
