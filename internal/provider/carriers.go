package provider

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
)

// captureCarriers copies opaque reasoning blocks byte-identically.
func captureCarriers(resp *Response, rt *Route) {
	if len(resp.Reasoning) == 0 || resp.ReasoningField != "reasoning" {
		return
	}
	var parts []json.RawMessage
	if err := json.Unmarshal(resp.Reasoning, &parts); err != nil {
		return
	}
	parts = flattenParts(parts)
	if len(parts) > 256 { // bound the array
		return
	}
	plain := make([]json.RawMessage, 0, len(parts))
	total := 0
	for i, p := range parts {
		kind, ok := carrierKind(p)
		if !ok {
			plain = append(plain, p)
			continue
		}
		raw := slices.Clone([]byte(p))
		if len(raw) > MaxCarrierBytes || total+len(raw) > MaxMessageBytes {
			resp.CarrierDrops = mergeDrop(resp.CarrierDrops, "too_large")
			continue
		}
		total += len(raw)
		resp.Carriers = append(resp.Carriers, Carrier{Family: rt.CarrierFamily(), Model: rt.Model, Kind: kind, Anchor: strconv.Itoa(i), Raw: raw, Replayable: true, SHA: payloadSHA(raw)})
	}
	if len(resp.Carriers) == 0 {
		return
	}
	if len(plain) == 0 {
		resp.Reasoning = nil
		return
	}
	encoded, err := json.Marshal(plain)
	if err == nil {
		resp.Reasoning = encoded
	}
}

func carrierKind(part json.RawMessage) (CarrierKind, bool) {
	var obj struct {
		Type             string          `json:"type"`
		ThoughtSignature json.RawMessage `json:"thought_signature"`
		Signature        json.RawMessage `json:"signature"`
		EncryptedContent json.RawMessage `json:"encrypted_content"`
		ReasoningOpaque  json.RawMessage `json:"reasoning_opaque"`
		Data             json.RawMessage `json:"data"`
	}
	if json.Unmarshal(part, &obj) != nil {
		return "", false
	}
	switch {
	case len(obj.ThoughtSignature) > 0 && string(obj.ThoughtSignature) != "null":
		return CarrierThoughtSignature, true
	case len(obj.EncryptedContent) > 0 && string(obj.EncryptedContent) != "null":
		return CarrierEncrypted, true
	case len(obj.ReasoningOpaque) > 0 && string(obj.ReasoningOpaque) != "null":
		return CarrierOpaque, true
	case obj.Type == "redacted_thinking" && len(obj.Data) > 0 && string(obj.Data) != "null":
		return CarrierRedactedThinking, true
	case len(obj.Signature) > 0 && string(obj.Signature) != "null":
		switch obj.Type {
		case "redacted_thinking":
			return CarrierRedactedThinking, true
		default:
			return CarrierThinking, true
		}
	}
	return "", false
}

func payloadSHA(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

// projectCarriers requires an exact (family, model) replayable match.
func projectCarriers(rt *Route, cs []Carrier) ([]Carrier, []CarrierDrop, error) {
	keep := make([]Carrier, 0, len(cs))
	total := 0
	var drops []CarrierDrop
	for i := range cs {
		c := &cs[i]
		if !c.Replayable {
			drops = mergeDrop(drops, "redacted")
			continue
		}
		if !c.Kind.Valid() || len(c.Raw) == 0 || !json.Valid(c.Raw) {
			return nil, nil, fmt.Errorf("carrier %d is invalid (%q)", i, c.Kind)
		}
		if len(c.Raw) > MaxCarrierBytes || total+len(c.Raw) > MaxMessageBytes {
			drops = mergeDrop(drops, "too_large")
			continue
		}
		if rt.CarrierFamily() != c.Family || rt.Model != c.Model {
			drops = mergeDrop(drops, "route_mismatch")
			continue
		}
		total += len(c.Raw)
		keep = append(keep, *c)
	}
	return keep, drops, nil
}

func mergeDrop(drops []CarrierDrop, reason string) []CarrierDrop {
	for i := range drops {
		if drops[i].Reason == reason {
			drops[i].Count++
			return drops
		}
	}
	return append(drops, CarrierDrop{Reason: reason, Count: 1})
}

func mergeDrops(dst, src []CarrierDrop) []CarrierDrop {
	for _, d := range src {
		for range d.Count {
			dst = mergeDrop(dst, d.Reason)
		}
	}
	return dst
}

func withoutCarriers(r *Request) *Request {
	cp := *r
	cp.Messages = make([]Message, len(r.Messages))
	copy(cp.Messages, r.Messages)
	for i := range cp.Messages {
		cp.Messages[i].Carriers = nil
	}
	return &cp
}

func isSignatureInvalid(err error) bool {
	e, ok := errors.AsType[*Error](err)
	if !ok || e.HTTPStatus != http.StatusBadRequest {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(e.Code)) {
	case "invalid_signature", "signature_invalid":
		return true
	}
	return false
}

// flattenParts expands one level of nested arrays.
func flattenParts(in []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(in))
	for _, p := range in {
		var inner []json.RawMessage
		if len(p) > 0 && p[0] == '[' && json.Unmarshal(p, &inner) == nil && len(out)+len(inner) <= 256 {
			out = append(out, inner...)
			continue
		}
		out = append(out, p)
	}
	return out
}
