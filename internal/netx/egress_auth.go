//go:build !minimal

package netx

// addAuthEgress registers the OAuth credential transports only when the auth
// package is compiled in.
func addAuthEgress(reg map[string]Egress) {
	reg["auth"] = Egress{Purpose: Inference, Host: "the OAuth issuer's RFC 8414 endpoints (--issuer, token, device, revocation); never a wildcard", Trigger: "setup auth login/logout/status device flow, token refresh, or RFC 7009 revocation"}
	reg["web-oauth"] = Egress{Purpose: Background, Host: "the configured Nous Portal OAuth authorization/token endpoints; never a wildcard", Trigger: "web dashboard drain/nous login, authorization-code exchange, and token refresh"}
}
