//go:build !linux && !darwin

package exec

// markerPids: no portable process environment scan; Windows trees are
// killed by their Job Object (§A.4), other Unixes rely on the process group.
func markerPids(func([]byte) bool) ([]int, error) { return nil, nil }
