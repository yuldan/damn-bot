//go:build minimal

package netx

// addAuthEgress is a no-op: auth is not compiled into minimal.
func addAuthEgress(map[string]Egress) {}
