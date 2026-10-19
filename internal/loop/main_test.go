package loop

import (
	"flag"
	"testing"

	"go.uber.org/goleak"
)

// TestGoleakMain (E-29, NEW-32): the loop starts goroutines through boundary;
// every one must be gone when the test binary ends. The golden -update flag
// is registered here so it needs no package-level variable (E-10).
func TestMain(m *testing.M) {
	flag.Bool("update", false, "update golden files")
	flag.Parse()
	goleak.VerifyTestMain(m)
}
