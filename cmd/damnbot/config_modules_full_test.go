//go:build !minimal

package main

// wantConfigModules is the `config modules` inventory of the full build:
// the web, browser, media, voice and computer_use descriptors are registered,
// disabled by default.
func wantConfigModules() string {
	return "id\tgroup\tconfig\ttools\tenabled\nbrowser\tbrowser\tmodules.browser\t18\tfalse\ncomputer_use\tcomputer_use\tmodules.computer_use\t1\tfalse\nmedia\tmedia\tmodules.media\t4\tfalse\nvoice\tvoice\tmodules.voice\t2\tfalse\nweb\tweb\tmodules.web\t2\tfalse\n"
}
