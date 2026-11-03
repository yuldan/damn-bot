//go:build minimal

package main

// wantConfigModules is the `config modules` inventory of the minimal build:
// no module package is compiled in.
func wantConfigModules() string { return "no modules registered in this build\n" }
