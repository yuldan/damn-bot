//go:build !minimal

package main

// wantConfigModules is the `config modules` inventory of the full build:
// the web, browser, media, voice, computer_use and desktop_ui descriptors are
// registered, disabled by default.
func wantConfigModules() string {
	return "id\tgroup\tconfig\ttools\tenabled\nbrowser\tbrowser\tmodules.browser\t23\tfalse\ncomputer_use\tcomputer_use\tmodules.computer_use\t1\tfalse\nconnectors\tconnectors\tmodules.connectors\t14\tfalse\ndesktop_ui\tdesktop\tmodules.desktop_ui\t13\tfalse\nmedia\tmedia\tmodules.media\t8\tfalse\nvoice\tvoice\tmodules.voice\t2\tfalse\nweb\tweb\tmodules.web\t2\tfalse\n"
}
