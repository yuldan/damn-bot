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
	if err := json.Unmarshal(resp.Reasoning, &parts); err != nil || len(parts) > 256 { // bound the array
		return
	}
	family := rt.CarrierFamily()
	plain := make([]json.RawMessage, 0, len(parts))
	for i, p := range parts {
		kind, ok := carrierKind(p)
		if !ok {
			plain = append(plain, p)
			continue
		}
		raw := slices.Clone([]byte(p))
		resp.Carriers = append(resp.Carriers, Carrier{
			Family: family, Model: rt.Model, Kind: kind,
			Anchor: strconv.Itoa(i), Raw: raw, Replayable: true, SHA: payloadSHA(raw),
		})
	}
	if len(resp.Carriers) == 0 {
		return
	}
	if len(plain) == 0 {
		resp.Reasoning = nil
		return
	}
	encoded, err := json.Marshal(plain)
	if err != nil {
		resp.Reasoning = nil
		return
	}
	resp.Reasoning = encoded
}

func carrierKind(part json.RawMessage) (CarrierKind, bool) {
	var obj struct {
		Type             string          `json:"type"`
		ThoughtSignature json.RawMessage `json:"thought_signature"`
		Signature        json.RawMessage `json:"signature"`
		EncryptedContent json.RawMessage `json:"encrypted_content"`
		ReasoningOpaque  json.RawMessage `json:"reasoning_opaque"`
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
		if rt.CarrierFamily() != c.Family || rt.Model != c.Model {
			drops = mergeDrop(drops, "route_mismatch")
			continue
		}
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
