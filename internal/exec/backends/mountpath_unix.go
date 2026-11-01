//go:build !minimal && !windows

package backends

// bindMount keeps the workspace at the same path inside the container.
func bindMount(root string) (string, error) {
	return "type=bind,src=" + root + ",dst=" + root + ",rw", nil
}

// containerPath needs no translation: paths are identical.
func containerPath(host string) (string, error) { return host, nil }
