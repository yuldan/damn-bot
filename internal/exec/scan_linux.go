package exec

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"syscall"
)

// markerPids lists live processes whose environment carries MarkerEnv with
// a value accepted by match. Processes of other users and zombies (empty
// environ) are skipped; the scan is best effort by nature (§A.4).
func markerPids(match func(val []byte) bool) ([]int, error) {
	d, err := os.Open("/proc")
	if err != nil {
		return nil, fmt.Errorf("exec: sweep: %w", err)
	}
	names, err := d.Readdirnames(-1)
	if err = errors.Join(err, d.Close()); err != nil {
		return nil, fmt.Errorf("exec: sweep: %w", err)
	}
	self := os.Getpid()
	var (
		pids []int
		errs error
	)
	buf := make([]byte, 0, envScanBuf)
	for _, n := range names {
		pid, err := strconv.Atoi(n)
		if err != nil || pid == self {
			continue
		}
		buf, err = readEnviron(pid, buf[:0])
		errs = errors.Join(errs, err)
		if v, ok := markerValue(buf); ok && match(v) {
			pids = append(pids, pid)
		}
	}
	if errs != nil {
		errs = fmt.Errorf("exec: sweep: %w", errs)
	}
	return pids, errs
}

// envScanBuf is the initial environ read buffer; larger environs grow it.
const envScanBuf = 16 << 10

// readEnviron reads /proc/<pid>/environ into buf. Unreadable processes
// (other users, exited) yield an empty environ; only a failed close of an
// opened descriptor is an error.
func readEnviron(pid int, buf []byte) ([]byte, error) {
	f, err := os.Open("/proc/" + strconv.Itoa(pid) + "/environ")
	if err != nil {
		return buf, benignOpen(err)
	}
	for {
		if len(buf) == cap(buf) {
			buf = append(buf, 0)[:len(buf)]
		}
		n, rerr := f.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if rerr != nil || n == 0 {
			if err := f.Close(); err != nil {
				return buf, fmt.Errorf("exec: close environ: %w", err)
			}
			return buf, nil
		}
	}
}

// benignOpen drops the expected open failures of a process scan: exited,
// or owned by another user.
func benignOpen(err error) error {
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return fmt.Errorf("exec: open environ: %w", err)
}
