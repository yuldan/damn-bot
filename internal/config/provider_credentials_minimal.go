//go:build minimal

package config

// providerCredentials is empty: the minimal build compiles no credential
// pool (P13).
type providerCredentials struct{}

// credentialSources has no pool to count in the minimal build.
func credentialSources(*CustomProvider, string) (int, error) { return 0, nil }
