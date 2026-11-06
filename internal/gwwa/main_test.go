package gwwa_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain proves the client leaves no goroutine behind: Connect's pump and
// both transports end on Close.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
