package loop

import (
	"testing"

	"go.uber.org/goleak"
)

// TestGoleakMain (E-29, NEW-32): the loop starts goroutines through boundary;
// every one must be gone when the test binary ends.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
