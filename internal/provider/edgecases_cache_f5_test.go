package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestCacheMarkerCapability (EC-PROV-027): a route with the catalog "cache"
// capability emits exactly one cache_control breakpoint on the OpenAI wire,
// and a route without it emits no foreign field at all.
func TestCacheMarkerCapability(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-PROV-027", "F5", "covered") {
		return
	}
	openai := func(markers bool) *Route {
		return &Route{Provider: "kimi", Model: "claude", Family: "openai", CacheMarkers: markers}
	}
	t.Run("capability sends the marker", func(t *testing.T) {
		t.Parallel()
		body, err := encode(openai(true), boundaryRequest(1), nil)
		if err != nil || !json.Valid(body) {
			t.Fatalf("encode: %v", err)
		}
		if n := strings.Count(string(body), `"cache_control"`); n != 1 {
			t.Fatalf("markers = %d, want 1:\n%s", n, body)
		}
		var wire struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &wire); err != nil {
			t.Fatal(err)
		}
		var block []map[string]any
		if err := json.Unmarshal(wire.Messages[0].Content, &block); err != nil || len(block) != 1 {
			t.Fatalf("block = %s, %v", wire.Messages[0].Content, err)
		}
		if len(block[0]) != 3 || block[0]["type"] != "text" || block[0]["cache_control"] == nil {
			t.Fatalf("foreign fields in cache block: %#v", block[0])
		}
	})
	t.Run("no capability stays clean", func(t *testing.T) {
		t.Parallel()
		body, err := encode(openai(false), boundaryRequest(1), nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), "cache_control") {
			t.Fatalf("route without capability emitted cache_control:\n%s", body)
		}
	})
}
