//go:build !minimal

package netx

// addAuthEgress registers the OAuth credential transport only when the auth
// package is compiled in.
func addAuthEgress(reg map[string]Egress) {
	reg["auth"] = Egress{
		Purpose: Inference,
		Host:    "the OAuth issuer's RFC 8414 endpoints (--issuer, token, device, revocation); never a wildcard",
		Trigger: "setup auth login/logout/status device flow, token refresh, or RFC 7009 revocation",
	}
}
