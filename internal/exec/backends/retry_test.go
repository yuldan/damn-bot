//go:build unix

package backends

import (
	"fmt"
	"testing"

	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/testutil"
)

// runBackend calls b.Run and retries only ETXTBSY: the fixture CLI is written
// by the test process, so a fork from a parallel test can still hold its write
// descriptor when the backend execs it.
func runBackend(t *testing.T, b exec.Backend, spec *exec.Spec) (exec.Result, error) {
	t.Helper()
	var res exec.Result
	err := testutil.ExecRetry(t.Context(), func() error {
		var rerr error
		res, rerr = b.Run(t.Context(), spec)
		if rerr != nil {
			return fmt.Errorf("backend run: %w", rerr)
		}
		return nil
	})
	if err != nil {
		return res, fmt.Errorf("backend run: %w", err)
	}
	return res, nil
}
