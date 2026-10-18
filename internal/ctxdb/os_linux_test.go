//go:build linux

package ctxdb

import (
	"bytes"
	"os"
	"strconv"
)

// procState returns the state letter of /proc/<pid>/stat ("T" = stopped,
// "" = absent).
func procState(pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return ""
	}
	if i := bytes.LastIndexByte(b, ')'); i >= 0 && i+2 < len(b) {
		return string(b[i+2])
	}
	return ""
}
