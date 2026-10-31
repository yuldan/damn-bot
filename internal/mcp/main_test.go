package mcp

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain enforces E-29: every transport goroutine and child must be gone
// when the package's tests finish.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
