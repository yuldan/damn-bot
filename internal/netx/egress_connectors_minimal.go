//go:build minimal

package netx

// addConnectorEgress is a no-op: the connectors module is not compiled into
// minimal, so none of its call sites exist there.
func addConnectorEgress(map[string]Egress) {}
