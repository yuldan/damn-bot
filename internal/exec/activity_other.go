//go:build unix && !linux && !darwin

package exec

import "time"

// treeSampler: no accounting on this OS.
type treeSampler struct{}

// sampleTree: no portable accounting; the tree is never idle-killed.
func (t *treeSampler) sample(*osProc, string) (time.Duration, uint64, bool) { return 0, 0, false }

// treeWaitsInput: detection relies on prompt lines.
func treeWaitsInput(string, bool) bool { return false }
