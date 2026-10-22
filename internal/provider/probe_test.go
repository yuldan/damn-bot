package provider_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestNoContextProbe (GAP-COST-02, ADR-068 F1 subset): startup plus one
// openai-chat turn sends no request without user content. Construction opens
// no connection and the provider never probes the context window with a
// request of its own.
func TestNoContextProbe(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, providertest.Text("ok"))
	o := client(t, &provider.Config{Routes: []provider.Route{route("m", srv.URL)}})
	if n := len(srv.Requests()); n != 0 {
		t.Fatalf("startup made %d provider requests, want 0", n)
	}
	if _, err := o.Do(t.Context(), provider.PurposeTurn, ask("fake/m"), nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	reqs := srv.Requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests after one turn, want 1", len(reqs))
	}
	for _, raw := range reqs {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("request body %s: %v", raw, err)
		}
		user := false
		for _, m := range body.Messages {
			if m.Role == "user" && strings.TrimSpace(m.Content) != "" {
				user = true
			}
		}
		if !user {
			t.Fatalf("request without user content: %s", raw)
		}
	}
}
