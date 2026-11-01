//go:build minimal

package doctor

// credentialPoolOptions is empty: the minimal build compiles no pool.
type credentialPoolOptions struct{}

// credentialPoolChecks adds no check in the minimal build.
func credentialPoolChecks(*Options) []Check { return nil }
