//go:build !minimal && windows

package backends

import (
	"fmt"
	"strings"

	"github.com/damnbot/damnbot/internal/exec"
)

// bindMount translates the workspace path for a Linux container: Docker on
// Windows rejects `C:\...` destinations, so the dst is the /c/... form and the
// src keeps the drive-letter form Docker Desktop understands.
func bindMount(root string) (string, error) {
	dst, err := containerPath(root)
	if err != nil {
		return "", err
	}
	src := strings.ReplaceAll(stripExtendedPath(root), `\`, "/")
	return "type=bind,src=" + src + ",dst=" + dst + ",rw", nil
}

// stripExtendedPath removes the \\?\ and \\??\ prefixes filepath.EvalSymlinks
// returns on Windows; Docker Desktop accepts only drive-letter/UNC paths.
func stripExtendedPath(host string) string {
	for _, prefix := range []string{`\\?\UNC\`, `\\?\`, `\\??\`} {
		if strings.HasPrefix(host, prefix) {
			host = strings.TrimPrefix(host, prefix)
			if prefix == `\\?\UNC\` {
				return `\\` + host
			}
			return host
		}
	}
	return host
}

// containerPath maps C:\dir to /c/dir. UNC/device paths are refused with a
// clear error instead of producing a silent empty mount.
func containerPath(host string) (string, error) {
	host = stripExtendedPath(host)
	if len(host) < 3 || host[1] != ':' || (host[2] != '\\' && host[2] != '/') {
		return "", fmt.Errorf("%w: %q is not a drive-letter path a Linux container can mount", exec.ErrCapability, host)
	}
	drive := host[0]
	if drive >= 'A' && drive <= 'Z' {
		drive += 'a' - 'A'
	}
	rest := strings.ReplaceAll(host[3:], `\`, "/")
	if rest == "" {
		return "/" + string(drive), nil
	}
	return "/" + string(drive) + "/" + rest, nil
}
