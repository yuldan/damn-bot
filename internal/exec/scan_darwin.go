package exec

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// markerPids lists this user's processes whose environment carries
// MarkerEnv with a value accepted by match (kern.procargs2, §A.4).
func markerPids(match func(val []byte) bool) ([]int, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Getuid())
	if err != nil {
		return nil, fmt.Errorf("exec: sweep: %w", err)
	}
	self := os.Getpid()
	var pids []int
	for i := range procs {
		pid := int(procs[i].Proc.P_pid)
		if pid == self || pid <= 0 {
			continue
		}
		raw, err := unix.SysctlRaw("kern.procargs2", pid)
		if err != nil {
			continue // exited or not ours
		}
		if v, ok := markerValue(procEnv(raw)); ok && match(v) {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// procEnv returns the NUL-separated environment block of a procargs2
// buffer: int32 argc, exec path, NUL padding, argc argv strings, env.
func procEnv(raw []byte) []byte {
	const argcLen = 4
	if len(raw) < argcLen {
		return nil
	}
	argc := int(binary.LittleEndian.Uint32(raw))
	rest := raw[argcLen:]
	_, rest, _ = bytes.Cut(rest, []byte{0}) // exec path
	rest = bytes.TrimLeft(rest, "\x00")
	for ; argc > 0 && len(rest) > 0; argc-- {
		_, rest, _ = bytes.Cut(rest, []byte{0})
	}
	return rest
}
