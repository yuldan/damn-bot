//go:build linux

package ctxdb

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unsafe"
)

const (
	clockMonotonic = 1 // CLOCK_MONOTONIC: stops during suspend
	clockBoottime  = 7 // CLOCK_BOOTTIME: runs during suspend
)

func clockMs(id uintptr) int64 {
	var ts syscall.Timespec
	// clock_gettime cannot fail for these ids on Linux ≥ 2.6.39.
	_, _, _ = syscall.Syscall(syscall.SYS_CLOCK_GETTIME, id, uintptr(unsafe.Pointer(&ts)), 0)
	return ts.Sec*1000 + ts.Nsec/1e6
}

// BootMs returns CLOCK_BOOTTIME in ms.
func (SystemClock) BootMs() int64 { return clockMs(clockBoottime) }

// MonoMs returns CLOCK_MONOTONIC in ms.
func (SystemClock) MonoMs() int64 { return clockMs(clockMonotonic) }

// SelfIdent returns the identity of the calling process.
func SelfIdent() Ident {
	host, _ := os.Hostname()
	ns := ""
	if fi, err := os.Stat("/proc/self/ns/pid"); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			ns = strconv.FormatUint(st.Ino, 10)
		}
	}
	start, _ := procStart(os.Getpid())
	b, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return Ident{Host: host + "#" + ns, PID: os.Getpid(), Start: start, BootID: strings.TrimSpace(string(b))}
}

// procStart reads field 22 (starttime) of /proc/<pid>/stat.
func procStart(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	i := strings.LastIndexByte(string(b), ')')
	f := strings.Fields(string(b[i+1:]))
	if i < 0 || len(f) < 20 {
		return "", nil
	}
	return f[19], nil // field 22 = 20th after "pid (comm)"
}

// Gone reports an owner on this host as provably absent (SPEC §11.11.1):
// PID missing, or both start stamps known and different. Unknown → present.
func (SystemProcs) Gone(id Ident) bool {
	st, err := procStart(id.PID)
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if err != nil {
		return false
	}
	return id.Start != "" && st != "" && st != id.Start
}

// pgroupMembers lists live processes whose process group is pgid.
// Used instead of "leader alive" (AF-07): a pgid cannot be reused while the
// group still has members, so membership alone is the anti-reuse check.
func pgroupMembers(pgid int) []int {
	ents, _ := os.ReadDir("/proc")
	var out []int
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		i := strings.LastIndexByte(string(b), ')')
		if err != nil || i < 0 {
			continue
		}
		f := strings.Fields(string(b[i+1:]))
		if len(f) > 2 && f[2] == strconv.Itoa(pgid) && f[0] != "Z" {
			out = append(out, pid)
		}
	}
	return out
}

// killGroup SIGKILLs process group pgid if a member started at or after
// startStamp is alive (AF-07 instead of "leader still alive").
func killGroup(pgid int, startStamp string) bool {
	want, _ := strconv.ParseUint(startStamp, 10, 64)
	for _, pid := range pgroupMembers(pgid) {
		st, _ := procStart(pid)
		if got, _ := strconv.ParseUint(st, 10, 64); got >= want {
			return syscall.Kill(-pgid, syscall.SIGKILL) == nil
		}
	}
	return false
}
