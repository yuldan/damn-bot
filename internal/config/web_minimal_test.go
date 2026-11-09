//go:build minimal

package config

import "testing"

// TestWebSectionDecodesWithoutValidation: the minimal binary has no
// `serve --web`, so [web] keeps decoding into the shared type (a config file
// written for the full build still loads) but the fail-closed validation is
// full-build code and absent here (ADR-325). The defaults stay identical.
func TestWebSectionDecodesWithoutValidation(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, "[web]\nlisten = \"0.0.0.0:7878\"\n")
	if err != nil {
		t.Fatalf("minimal parse: %v", err)
	}
	if cfg.Web.Listen != "0.0.0.0:7878" {
		t.Fatalf("listen = %q, want 0.0.0.0:7878", cfg.Web.Listen)
	}
}
