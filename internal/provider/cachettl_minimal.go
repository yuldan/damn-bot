//go:build minimal

package provider

// CacheControl in the minimal build keeps the plain 5m ephemeral breakpoint:
// the TTL policy is part of the full adapter surface (GAP-COST-03).
func CacheControl(*Route, string) map[string]string {
	return map[string]string{"type": "ephemeral"}
}
