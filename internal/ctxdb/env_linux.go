//go:build linux

package ctxdb

import (
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// BootMs returns CLOCK_BOOTTIME in ms (runs during suspend).
func (SystemClock) BootMs() int64 { return clockMs(unix.CLOCK_BOOTTIME) }

// MonoMs returns CLOCK_MONOTONIC in ms (stops during suspend).
func (SystemClock) MonoMs() int64 { return clockMs(unix.CLOCK_MONOTONIC) }

// procFile reads a file below /proc.
func procFile(name string) (string, error) {
	b, err := fs.ReadFile(os.DirFS("/proc"), name)
	if err != nil {
		return "", fmt.Errorf("ctxdb: /proc/%s: %w", name, err)
	}
	return string(b), nil
}

// SelfIdent returns the identity of the calling process (§11.11.1).
func SelfIdent() Ident {
	host, herr := os.Hostname()
	if herr != nil {
		host = "" // host only scopes criterion 3 of alive()
	}
	ns := ""
	if fi, err := os.Stat("/proc/self/ns/pid"); err == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			ns = strconv.FormatUint(st.Ino, 10)
		}
	}
	start, _, serr := procStat(os.Getpid())
	if serr != nil {
		start = "" // unknown start stamp = "present" for observers
	}
	boot, berr := procFile("sys/kernel/random/boot_id")
	if berr != nil {
		boot = "" // unknown boot id: clock-free rule of §11.11.2 applies
	}
	return Ident{Host: host + "#" + ns, PID: os.Getpid(), Start: start, BootID: strings.TrimSpace(boot)}
}

// procStat returns field 22 (starttime) and field 5 (pgrp) of
// /proc/<pid>/stat; zombies count as absent.
func procStat(pid int) (start, pgrp string, err error) {
	s, err := procFile(strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", "", err
	}
	f := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
	if len(f) < 20 || f[0] == "Z" {
		return "", "", fmt.Errorf("ctxdb: pid %d: %w", pid, fs.ErrNotExist)
	}
	return f[19], f[2], nil
}

// pgroupMembers maps live members of process group pgid to their start
// stamps. Used instead of "leader alive" (AF-07): a pgid is not reused while
// the group still has members, so membership is the anti-reuse check.
func pgroupMembers(pgid int) map[int]string {
	ents, err := os.ReadDir("/proc")
	out := map[int]string{}
	for i := 0; err == nil && i < len(ents); i++ {
		pid, perr := strconv.Atoi(ents[i].Name())
		if perr != nil {
			continue
		}
		if st, pg, serr := procStat(pid); serr == nil && pg == strconv.Itoa(pgid) {
			out[pid] = st
		}
	}
	return out
}
