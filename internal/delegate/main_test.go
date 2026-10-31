package delegate_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain enforces E-29: the worktree manager and agent goroutines must all
// be joined before the package exits.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
