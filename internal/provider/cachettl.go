//go:build !minimal

package provider

import "time"

// InteractiveCacheTTL is the 1h breakpoint of an interactive origin
// (GAP-COST-03); a batch origin keeps the 5m default.
const InteractiveCacheTTL = time.Hour

// CacheControl is the cache_control object of a breakpoint: the 1h TTL for
// an interactive session origin when the catalog allows it, else the 5m
// default with no ttl field (byte-compatible with third-party
// anthropic-messages endpoints). CacheTTLMax clamps the request: a catalog
// row that claims less than an hour can never be asked for one.
func CacheControl(rt *Route, origin string) map[string]string {
	if interactiveOrigin(origin) && rt.CacheTTLMax >= int64(InteractiveCacheTTL/time.Second) {
		return map[string]string{"type": "ephemeral", "ttl": "1h"}
	}
	return map[string]string{"type": "ephemeral"}
}

// interactiveOrigin reports the SPEC §3 sessions.origin values whose prompt
// prefix is reused across turns with human-paced gaps.
func interactiveOrigin(origin string) bool {
	switch origin {
	case "tui", "acp", "web", "gateway":
		return true
	}
	return false
}
