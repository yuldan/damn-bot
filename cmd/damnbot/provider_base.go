//go:build !acpe2e

package main

// e2eProviderBase is the test-only provider base URL override (see
// provider_base_e2e.go). The shipped binary never overrides the route.
func e2eProviderBase() string { return "" }
