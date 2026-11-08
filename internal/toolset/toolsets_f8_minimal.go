//go:build minimal

package toolset

// f8Toolsets is empty in the minimal build: the connectors module is not
// compiled in, so its toolsets cannot be selected there.
func f8Toolsets() []Definition { return nil }
