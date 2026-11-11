//go:build !minimal

package main

import (
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/toolset"
)

// TestDesktopModuleGate: the GUI module is default off and turns on only for a
// selected desktop_ui/project/onboarding toolset or a custom entry naming one
// of its tools.
func TestDesktopModuleGate(t *testing.T) {
	var empty config.Config
	if desktopUIEnabled(&empty) {
		t.Fatal("desktop module enabled without a toolset")
	}
	for _, name := range []string{"desktop_ui", "project", "onboarding"} {
		cfg := config.Config{Toolsets: toolset.Config{Active: []string{name}}}
		if !desktopUIEnabled(&cfg) {
			t.Fatalf("%s did not enable the desktop module", name)
		}
	}
	custom := config.Config{Toolsets: toolset.Config{Custom: map[string]toolset.Definition{
		"mine": {Name: "mine", Tools: []string{"read_terminal"}},
	}}}
	if !desktopUIEnabled(&custom) {
		t.Fatal("custom desktop tool did not enable the module")
	}
}
