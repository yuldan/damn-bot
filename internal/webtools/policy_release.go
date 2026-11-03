//go:build !minimal && !webtest

package webtools

// defaultPolicy is the production SSRF gate: only public hosts are fetched
// (SPEC §14.5). The `webtest` tag replaces it in the e2e build so the real
// binary can fetch from a loopback fixture.
func defaultPolicy() hostPolicy { return publicHost }

// testEgress reports whether the tool-egress client may dial loopback. It is
// false in every production build; only `-tags webtest` flips it.
func testEgress() bool { return false }
