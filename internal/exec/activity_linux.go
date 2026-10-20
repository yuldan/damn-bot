package exec

import (
	"bytes"
	"io/fs"
	"math"
	"os"
	"strconv"
	"time"
)

// pidUsage is the cumulative CPU time and I/O bytes of one process.
type pidUsage struct {
	cpu time.Duration
	io  uint64
}

// treeSampler holds the per-pid counters of the previous sample.
type treeSampler struct{ prev map[int]pidUsage }

// deltaPids turns per-pid cumulative counters into the tree's delta since
// the previous sample; a new pid contributes its whole usage.
func (t *treeSampler) deltaPids(cur map[int]pidUsage) (time.Duration, uint64) {
	var cpu time.Duration
	var io uint64
	for pid, u := range cur {
		p := t.prev[pid]
		if u.cpu > p.cpu {
			cpu += u.cpu - p.cpu
		}
		if u.io > p.io {
			io += u.io - p.io
		}
	}
	t.prev = cur
	return cpu, io
}

// clockTick is USER_HZ, 100 on every Linux ABI Go supports.
const clockTick = 10 * time.Millisecond

// sampleTree returns the CPU time and I/O bytes the tree (processes with
// the call's marker) used since the previous sample.
func (t *treeSampler) sample(_ *osProc, id string) (time.Duration, uint64, bool) {
	pids, err := markerPids(func(v []byte) bool { return string(v) == id })
	if err != nil {
		return 0, 0, false
	}
	cur := make(map[int]pidUsage, len(pids))
	for _, pid := range pids {
		cur[pid] = procUsage(pid)
	}
	cpu, io := t.deltaPids(cur)
	return cpu, io, true
}

// procUsage reads utime+stime (/proc/<pid>/stat fields 14, 15) and
// rchar+wchar (/proc/<pid>/io); unreadable fields count as 0.
func procUsage(pid int) pidUsage {
	proc := os.DirFS("/proc/" + strconv.Itoa(pid))
	var u pidUsage
	if st, err := fs.ReadFile(proc, "stat"); err == nil {
		u.cpu = statCPU(st)
	}
	if io, err := fs.ReadFile(proc, "io"); err == nil {
		for line := range bytes.SplitSeq(io, []byte{'\n'}) {
			k, v, ok := bytes.Cut(line, []byte(": "))
			if ok && (string(k) == "rchar" || string(k) == "wchar") {
				u.io += parseCount(v)
			}
		}
	}
	return u
}

// statCPU is utime+stime of a /proc/<pid>/stat line (fields 14 and 15,
// counted after the parenthesized comm, which may hold spaces).
func statCPU(st []byte) time.Duration {
	i := bytes.LastIndexByte(st, ')')
	if i < 0 {
		return 0
	}
	f := bytes.Fields(st[i+1:])
	if len(f) <= 12 {
		return 0
	}
	ticks := parseCount(f[11]) + parseCount(f[12])
	if ticks > math.MaxInt32 {
		return 0
	}
	return time.Duration(ticks) * clockTick
}

// parseCount parses a /proc counter; malformed is 0.
func parseCount(b []byte) uint64 {
	n, err := strconv.ParseUint(string(b), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// treeWaitsInput reports whether some tree process is blocked reading a
// terminal or pipe (/proc/<pid>/wchan, §A.6). On a PTY, waiting in
// poll/select/epoll also counts: full-screen programs (vim, top) wait for
// keys that way (ADR-105 butir 17).
func treeWaitsInput(id string, pty bool) bool {
	pids, err := markerPids(func(v []byte) bool { return string(v) == id })
	if err != nil {
		return false
	}
	for _, pid := range pids {
		w, err := fs.ReadFile(os.DirFS("/proc/"+strconv.Itoa(pid)), "wchan")
		if err != nil {
			continue
		}
		// Compiler suffixes (".constprop.0", ".isra.0") are not part of the name.
		name, _, _ := bytes.Cut(bytes.TrimSpace(w), []byte{'.'})
		switch string(name) {
		case "n_tty_read", "wait_woken", "pipe_read":
			return true
		case "do_select", "do_sys_poll", "poll_schedule_timeout", "ep_poll", "do_epoll_wait":
			if pty {
				return true
			}
		}
	}
	return false
}
