//go:build !minimal

package main

// wantConfigModules is the `config modules` inventory of the full build:
// the web descriptor is registered, disabled by default.
func wantConfigModules() string {
	return "id\tgroup\tconfig\ttools\tenabled\nweb\tweb\tmodules.web\t2\tfalse\n"
}
