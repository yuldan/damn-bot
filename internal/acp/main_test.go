package acp

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain enforces E-29: the package spawns no goroutines that outlive the
// test binary. Framing is synchronous; the session registry only owns
// channels.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
