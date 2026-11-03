package computeruse

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain keeps the package honest about goroutines: the process backend
// drains stderr and waits for a child, so every test must leave none behind.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
