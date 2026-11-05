//go:build linux

package exec

import (
	"bytes"
	"io/fs"
	"os"
	"strconv"
)

// isZombie reports whether pid has exited and waits for its parent to reap
// it (state Z in /proc/<pid>/stat). The comm field may contain spaces and
// parentheses, so the state is looked up after its last ')'.
func isZombie(pid int) bool {
	b, err := fs.ReadFile(os.DirFS("/proc"), strconv.Itoa(pid)+"/stat")
	if err != nil {
		return false
	}
	i := bytes.LastIndexByte(b, ')')
	return i >= 0 && i+2 < len(b) && b[i+2] == 'Z'
}
