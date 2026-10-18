//go:build darwin

package ctxdb

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// darwinSZomb is SZOMB of <sys/proc.h> (exited, not yet reaped); x/sys does
// not export the p_stat values.
const darwinSZomb = 5

// BootMs returns CLOCK_MONOTONIC in ms: on macOS it is mach_continuous_time,
// which keeps running during sleep (the Linux CLOCK_BOOTTIME role, §11.11.1).
func (SystemClock) BootMs() int64 { return clockMs(unix.CLOCK_MONOTONIC) }

// MonoMs returns CLOCK_UPTIME_RAW in ms: mach_absolute_time, which stops
// during sleep (the Linux CLOCK_MONOTONIC role, §11.11.2 rule (b)).
func (SystemClock) MonoMs() int64 { return clockMs(unix.CLOCK_UPTIME_RAW) }

// SelfIdent returns the identity of the calling process (§11.11.1). macOS has
// no PID namespaces, so the host suffix is empty.
func SelfIdent() Ident {
	host, herr := os.Hostname()
	if herr != nil {
		host = "" // host only scopes criterion 3 of alive()
	}
	start, _, serr := procStat(os.Getpid())
	if serr != nil {
		start = "" // unknown start stamp = "present" for observers
	}
	boot, berr := unix.Sysctl("kern.bootsessionuuid")
	if berr != nil {
		boot = "" // unknown boot id: clock-free rule of §11.11.2 applies
	}
	return Ident{Host: host + "#", PID: os.Getpid(), Start: start, BootID: boot}
}

// kinfoStart encodes p_starttime (the value proc_pidinfo reports as
// pbi_start_tvsec/pbi_start_tvusec) losslessly as decimal microseconds, so
// killGroup can order stamps; it is recorded at fork and never re-read from
// the wall clock [INFERENCE, F1: verified on the macOS CI runner].
func kinfoStart(p *unix.KinfoProc) string {
	tv := p.Proc.P_starttime
	return strconv.FormatInt(int64(tv.Sec)*1_000_000+int64(tv.Usec), 10)
}

// procKinfo reads the kinfo_proc of pid; missing pids and zombies wrap
// fs.ErrNotExist, any other failure is "unknown".
func procKinfo(pid int) (*unix.KinfoProc, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("ctxdb: pid %d: %w", pid, fs.ErrNotExist)
	}
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		// A missing pid yields an empty record (EIO from x/sys); only
		// kill(pid, 0) == ESRCH proves absence.
		if errors.Is(unix.Kill(pid, 0), unix.ESRCH) {
			return nil, fmt.Errorf("ctxdb: pid %d: %w", pid, fs.ErrNotExist)
		}
		return nil, fmt.Errorf("ctxdb: sysctl kern.proc.pid %d: %w", pid, err)
	}
	if k.Proc.P_stat == darwinSZomb {
		return nil, fmt.Errorf("ctxdb: pid %d: %w", pid, fs.ErrNotExist)
	}
	return k, nil
}

// procStat returns the start stamp and process group of pid (zombies count
// as absent), mirroring the Linux /proc/<pid>/stat probe.
func procStat(pid int) (start, pgrp string, err error) {
	k, err := procKinfo(pid)
	if err != nil {
		return "", "", err
	}
	return kinfoStart(k), strconv.Itoa(int(k.Eproc.Pgid)), nil
}

// pgroupMembers maps live (non-zombie) members of process group pgid to
// their start stamps (AF-07: membership, not "leader alive"). A failed
// sysctl yields no members, as an unreadable /proc does on Linux.
func pgroupMembers(pgid int) map[int]string {
	out := map[int]string{}
	if pgid <= 0 {
		return out
	}
	ks, err := unix.SysctlKinfoProcSlice("kern.proc.pgrp", pgid)
	for i := 0; err == nil && i < len(ks); i++ {
		if p := &ks[i]; int(p.Eproc.Pgid) == pgid && p.Proc.P_stat != darwinSZomb {
			out[int(p.Proc.P_pid)] = kinfoStart(p)
		}
	}
	return out
}
