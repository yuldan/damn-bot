//go:build minimal

package provider_test

import "testing"

// ecVisionGate is not applicable to the minimal build (no image channel).
func ecVisionGate(t *testing.T) { t.Skip("minimal build has no vision route") }
