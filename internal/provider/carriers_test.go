package provider

import (
	"bytes"
	"encoding/json"
	"testing"
)

func carrierRequest(rt *Route, c *Carrier) *Request {
	return &Request{
		Model: rt.Ref(),
		Messages: []Message{
			{Role: "user", Content: "hi"},
			{Role: "assistant", Carriers: []Carrier{*c}},
		},
	}
}

func thinkingCarrier() Carrier {
	raw := []byte(`{"type":"thinking","thinking":"plan","signature":"sig-abc"}`)
	return Carrier{Family: "anthropic", Model: "claude", Kind: CarrierThinking, Anchor: "0", Raw: raw, Replayable: true, SHA: payloadSHA(raw)}
}

func TestCarrierReplaySameRoute(t *testing.T) {
	t.Parallel()
	rt := &Route{Provider: "anthropic", Model: "claude", Family: "anthropic"}
	body, sent, drops, err := encodeCarriers(rt, carrierRequest(rt, new(thinkingCarrier())), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || len(drops) != 0 {
		t.Fatalf("sent/drops = %d/%v", len(sent), drops)
	}
	if !bytes.Contains(body, []byte(`"signature":"sig-abc"`)) || !bytes.Contains(body, []byte(`"reasoning":[`)) {
		t.Fatalf("carrier not replayed: %s", body)
	}
}

func TestCarrierReplayDropsAcrossRoutes(t *testing.T) {
	t.Parallel()
	c := thinkingCarrier()
	for _, rt := range []*Route{
		{Provider: "gemini", Model: "claude", Family: "gemini"},         // family differs
		{Provider: "anthropic", Model: "claude-2", Family: "anthropic"}, // model differs
		{Provider: "anthropic-proxy", Model: "claude"},                  // empty family -> provider, no prefix match
		{Provider: "reseller", Model: "claude"},                         // no inheritance
	} {
		body, sent, drops, err := encodeCarriers(rt, carrierRequest(rt, &c), nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(sent) != 0 || !bytes.Contains(body, []byte(`"hi"`)) || bytes.Contains(body, []byte("sig-abc")) {
			t.Errorf("route %s: sent=%d body=%s", rt.Ref(), len(sent), body)
		}
		if len(drops) != 1 || drops[0].Reason != "route_mismatch" || drops[0].Count != 1 {
			t.Errorf("route %s: drops=%v", rt.Ref(), drops)
		}
	}
}

func TestCarrierRedactedDropped(t *testing.T) {
	t.Parallel()
	rt := &Route{Provider: "anthropic", Model: "claude", Family: "anthropic"}
	c := thinkingCarrier()
	c.Replayable = false
	body, sent, drops, err := encodeCarriers(rt, carrierRequest(rt, &c), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 0 || bytes.Contains(body, []byte("sig-abc")) {
		t.Fatalf("redacted carrier sent: %s", body)
	}
	if len(drops) != 1 || drops[0].Reason != "redacted" {
		t.Fatalf("drops = %v", drops)
	}
}

func TestCarrierCapture(t *testing.T) {
	t.Parallel()
	resp := &Response{ReasoningField: "reasoning", Reasoning: json.RawMessage(`[
		{"type":"thinking","thinking":"plan","signature":"sig"},
		{"type":"text","text":"plain"},
		{"type":"reasoning","encrypted_content":"cipher"}
	]`)}
	rt := &Route{Provider: "anthropic", Model: "claude", Family: "anthropic"}
	captureCarriers(resp, rt)
	if len(resp.Carriers) != 2 {
		t.Fatalf("carriers = %+v", resp.Carriers)
	}
	if resp.Carriers[0].Kind != CarrierThinking || resp.Carriers[1].Kind != CarrierEncrypted {
		t.Errorf("kinds = %s/%s", resp.Carriers[0].Kind, resp.Carriers[1].Kind)
	}
	if resp.Carriers[0].Family != "anthropic" || resp.Carriers[0].Model != "claude" || resp.Carriers[0].Anchor != "0" ||
		resp.Carriers[1].Anchor != "2" || resp.Carriers[0].SHA == "" {
		t.Errorf("carrier metadata = %+v", resp.Carriers)
	}
	if !bytes.Contains(resp.Reasoning, []byte(`"plain"`)) || bytes.Contains(resp.Reasoning, []byte("sig")) ||
		bytes.Contains(resp.Reasoning, []byte("cipher")) {
		t.Errorf("plain reasoning = %s", resp.Reasoning)
	}
}
