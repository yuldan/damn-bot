//go:build !minimal

package main

// wantConfigModules is the `config modules` inventory of the full build:
// the web and browser descriptors are registered, disabled by default.
func wantConfigModules() string {
	return "id\tgroup\tconfig\ttools\tenabled\nbrowser\tbrowser\tmodules.browser\t5\tfalse\nweb\tweb\tmodules.web\t2\tfalse\n"
}
