package main

import (
	"testing"

	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestAuxRouteSharesRuntimeRegion (EC-PROV-026): an auxiliary (light) route
// must not sign for a different Bedrock region than the main runtime route.
// routeRegion reads the region from the endpoint and shareRegion copies the
// primary region onto a Bedrock aux route; non-Bedrock routes keep no region.
func TestAuxRouteSharesRuntimeRegion(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-PROV-026", "F5", "covered") {
		return
	}
	t.Run("EC-PROV-026", func(t *testing.T) {
		t.Parallel()
		for base, want := range map[string]string{
			"https://bedrock-runtime.us-east-1.amazonaws.com":         "us-east-1",
			"https://bedrock-runtime.eu-central-1.amazonaws.com.cn":   "eu-central-1",
			"https://bedrock-runtime.ap-southeast-2.amazonaws.com/v1": "ap-southeast-2",
			"https://api.anthropic.com":                               "",
			"https://bedrock-runtime.amazonaws.com":                   "",
			"not a url":                                               "",
		} {
			if got := routeRegion(base); got != want {
				t.Errorf("routeRegion(%q) = %q, want %q", base, got, want)
			}
		}
		primary := provider.Route{Provider: "bedrock", Region: "us-east-1"}
		aux := provider.Route{Provider: "bedrock", Region: "eu-west-1"}
		if got := shareRegion(&primary, &aux); got.Region != "us-east-1" {
			t.Fatalf("aux region = %q, want the runtime region us-east-1", got.Region)
		}
		// Negative control: a non-Bedrock aux route has no region to inherit.
		other := provider.Route{Provider: "openai"}
		if got := shareRegion(&primary, &other); got.Region != "" {
			t.Fatalf("non-Bedrock aux region = %q, want empty", got.Region)
		}
	})
}
