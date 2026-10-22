//go:build windows

package ctxdb

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"strconv"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// BootMs returns GetTickCount64 in ms: time since boot including sleep and
// hibernation (the Linux CLOCK_BOOTTIME role, SPEC §11.11.1).
func (SystemClock) BootMs() int64 { return windows.DurationSinceBoot().Milliseconds() }

// MonoMs returns QueryUnbiasedInterruptTime in ms: it excludes sleep and
// hibernation (the Linux CLOCK_MONOTONIC role, §11.11.2 rule (b)). 0 on
// failure, as clockMs on Linux/macOS: a constant clock never satisfies the
// 3 s two-observation gap, so takeover case (b) fails closed.
func (SystemClock) MonoMs() int64 {
	ms, err := unbiasedMs()
	if err != nil {
		return 0
	}
	return ms
}

// unbiasedMs calls QueryUnbiasedInterruptTime. The API writes a ULONGLONG
// through a pointer and E-38 forbids unsafe, so the out-parameter lives in
// OS-allocated memory (never moved by the Go GC) and is copied back with
// ReadProcessMemory on our own process.
func unbiasedMs() (ms int64, err error) {
	buf, err := windows.LocalAlloc(windows.LPTR, 8)
	if err != nil {
		return 0, fmt.Errorf("ctxdb: LocalAlloc: %w", err)
	}
	defer func() {
		if _, ferr := windows.LocalFree(windows.Handle(buf)); ferr != nil {
			ms, err = 0, errors.Join(err, fmt.Errorf("ctxdb: LocalFree: %w", ferr))
		}
	}()
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("QueryUnbiasedInterruptTime")
	if ok, _, cerr := proc.Call(buf); ok == 0 { // also 0 when the export cannot be resolved
		return 0, fmt.Errorf("ctxdb: QueryUnbiasedInterruptTime: %w", cerr)
	}
	var out [8]byte
	if err := windows.ReadProcessMemory(windows.CurrentProcess(), buf, &out[0], uintptr(len(out)), nil); err != nil {
		return 0, fmt.Errorf("ctxdb: ReadProcessMemory: %w", err)
	}
	v := binary.LittleEndian.Uint64(out[:]) / 10_000 // 100 ns units → ms
	if v > math.MaxInt64 {
		return 0, errors.New("ctxdb: QueryUnbiasedInterruptTime out of range")
	}
	return int64(v), nil
}

// SelfIdent returns the identity of the calling process (§11.11.1). Windows
// has no PID namespaces, so the host suffix is empty.
func SelfIdent() Ident {
	host, herr := os.Hostname()
	if herr != nil {
		host = "" // host only scopes criterion 3 of alive()
	}
	start, serr := procStart(os.Getpid())
	if serr != nil {
		start = "" // unknown start stamp = "present" for observers
	}
	boot, berr := bootID()
	if berr != nil {
		boot = "" // unknown boot id: clock-free rule of §11.11.2 applies
	}
	return Ident{Host: host + "#", PID: os.Getpid(), Start: start, BootID: boot}
}

// bootID reads the kernel boot counter named by SPEC §11.11.1
// (PrefetchParameters\BootId) [INFERENCE, F1: verified on the Windows CI
// runner].
func bootID() (string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SYSTEM\CurrentControlSet\Control\Session Manager\Memory Management\PrefetchParameters`, registry.QUERY_VALUE)
	if err != nil {
		return "", fmt.Errorf("ctxdb: BootId key: %w", err)
	}
	v, _, err := k.GetIntegerValue("BootId")
	if err = errors.Join(err, k.Close()); err != nil {
		return "", fmt.Errorf("ctxdb: BootId: %w", err)
	}
	return strconv.FormatUint(v, 10), nil
}

// procStart returns the CreationTime FILETIME of pid as a decimal 100 ns
// count, verbatim (SPEC §11.11.1). Missing pids (ERROR_INVALID_PARAMETER) and
// exited processes still held open by a handle (the Windows "zombie") wrap
// fs.ErrNotExist; any other failure, e.g. access denied, is "unknown".
func procStart(pid int) (string, error) {
	if pid <= 0 || uint64(pid) > math.MaxUint32 {
		return "", fmt.Errorf("ctxdb: pid %d: %w", pid, fs.ErrNotExist)
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return "", fmt.Errorf("ctxdb: pid %d: %w", pid, fs.ErrNotExist)
	}
	if err != nil {
		return "", fmt.Errorf("ctxdb: OpenProcess %d: %w", pid, err)
	}
	st, serr := handleStart(h, pid)
	if jerr := errors.Join(serr, windows.CloseHandle(h)); jerr != nil {
		return "", jerr
	}
	return st, nil
}

// Wait results of WaitForSingleObject (x/sys types WAIT_TIMEOUT as an errno,
// so the uint32 results get local names).
const (
	waitObject0 = 0x00000000
	waitTimeout = 0x00000102
)

// handleStart reads exit state and CreationTime through an open handle.
func handleStart(h windows.Handle, pid int) (string, error) {
	// GetExitCodeProcess reports STILL_ACTIVE (259) both for a running
	// process and for one that exited with 259, so the code alone cannot
	// answer "still alive" (EC-WIN-009). The process object is signaled
	// exactly when the process has exited; a non-zero wait means it is
	// still running.
	ev, werr := windows.WaitForSingleObject(h, 0)
	if werr != nil {
		return "", fmt.Errorf("ctxdb: WaitForSingleObject %d: %w", pid, werr)
	}
	switch ev {
	case waitTimeout: // still running
	case waitObject0: // exited, a 259 exit included
		return "", fmt.Errorf("ctxdb: pid %d exited: %w", pid, fs.ErrNotExist)
	default:
		return "", fmt.Errorf("ctxdb: WaitForSingleObject %d: status %#x", pid, ev)
	}
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return "", fmt.Errorf("ctxdb: GetProcessTimes %d: %w", pid, err)
	}
	return strconv.FormatUint(uint64(created.HighDateTime)<<32|uint64(created.LowDateTime), 10), nil
}

// Gone reports an owner on this host as provably absent (SPEC §11.11.1):
// PID missing or exited, or both start stamps known and different.
// Unknown → present.
func (SystemProcs) Gone(id Ident) bool {
	st, err := procStart(id.PID)
	if err != nil {
		return errors.Is(err, fs.ErrNotExist)
	}
	return id.Start != "" && st != "" && st != id.Start
}

// killGroup is platform-n/a on Windows: there are no POSIX process groups
// (no setpgid, no kill(-pgid)), so a recorded tool_pgid can be neither
// enumerated nor signalled (NEW-15). It reports false, as Linux/macOS do
// when no member can be found, so journal recovery proceeds exactly as in
// that case.
func killGroup(int, string) bool { return false }
