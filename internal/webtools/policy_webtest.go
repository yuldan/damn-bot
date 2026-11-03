//go:build webtest

package webtools

// defaultPolicy allows every host in the test build only: the e2e binary
// fetches a loopback httptest fixture through the real module and the real
// netx client. Production builds keep publicHost (policy_release.go).
func defaultPolicy() hostPolicy { return func(string) error { return nil } }

// testEgress lets the netx Tool edge dial loopback in the test build only.
func testEgress() bool { return true }
