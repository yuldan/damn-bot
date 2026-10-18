//go:build unix

package exec

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// osProc is one process tree: the leader runs in its own session, so its
// pid is the process group id and kill(-pgid) reaches every member that did
// not call setsid itself (the marker sweep covers those, §A.4).
type osProc struct {
	p   *os.Process
	out *os.File // read end: pipe or PTY master
}

// start launches s.Argv with stdin from in (nil: /dev/null) and stdout and
// stderr merged into one pipe, or into a PTY when s.PTY.
func start(s *Spec, in *os.File) (*osProc, error) {
	if s.PTY {
		return startPTY(s, in)
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("exec: pipe: %w", err)
	}
	stdin, closeIn, err := stdinFile(in)
	if err != nil {
		return nil, errors.Join(err, closeAll(r, w))
	}
	attr := &os.ProcAttr{
		Dir:   s.Dir,
		Env:   s.Env,
		Files: []*os.File{stdin, w, w},
		Sys:   sysAttr(false),
	}
	p, err := os.StartProcess(s.Argv[0], s.Argv, attr)
	cerr := errors.Join(closeAll(w), closeIn())
	if err != nil {
		return nil, errors.Join(fmt.Errorf("exec: start: %w", err), cerr, closeAll(r))
	}
	if cerr != nil {
		return nil, errors.Join(cerr, killLeader(p), closeAll(r))
	}
	return &osProc{p: p, out: r}, nil
}

func startPTY(s *Spec, in *os.File) (*osProc, error) {
	master, tty, err := pty.Open()
	if err != nil {
		return nil, fmt.Errorf("exec: pty: %w", err)
	}
	if serr := pty.Setsize(master, &pty.Winsize{Rows: ptyRows, Cols: ptyCols}); serr != nil {
		return nil, errors.Join(fmt.Errorf("exec: pty size: %w", serr), closeAll(master, tty))
	}
	stdin := tty
	if in != nil {
		stdin = in
	}
	attr := &os.ProcAttr{
		Dir:   s.Dir,
		Env:   s.Env,
		Files: []*os.File{stdin, tty, tty},
		Sys:   sysAttr(true),
	}
	p, err := os.StartProcess(s.Argv[0], s.Argv, attr)
	cerr := closeAll(tty)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("exec: start: %w", err), cerr, closeAll(master))
	}
	if cerr != nil {
		return nil, errors.Join(cerr, killLeader(p), closeAll(master))
	}
	return &osProc{p: p, out: master}, nil
}

// stdinFile returns in, or /dev/null opened for the child (EOF on read).
func stdinFile(in *os.File) (*os.File, func() error, error) {
	if in != nil {
		return in, func() error { return nil }, nil
	}
	f, err := os.Open(os.DevNull)
	if err != nil {
		return nil, nil, fmt.Errorf("exec: open %s: %w", os.DevNull, err)
	}
	return f, func() error { return closeAll(f) }, nil
}

// killLeader kills and reaps a leader whose start could not complete.
func killLeader(p *os.Process) error {
	err := p.Kill()
	_, werr := p.Wait()
	return errors.Join(err, werr)
}

func (u *osProc) output() *os.File   { return u.out }
func (u *osProc) terminate() error   { return u.signal(syscall.SIGTERM) }
func (u *osProc) kill() error        { return u.signal(syscall.SIGKILL) }
func (u *osProc) closeOutput() error { return closeAll(u.out) }
func (u *osProc) treeAlive() bool    { return syscall.Kill(-u.p.Pid, 0) == nil }
func (u *osProc) release() error     { return nil }

// closeConsole: the PTY master is closed by closeOutput.
func (u *osProc) closeConsole() error { return nil }

func (u *osProc) signal(sig syscall.Signal) error {
	// ESRCH: the group is gone. EPERM: no member may be signalled — macOS
	// reports it for a group of zombies, and a member that changed uid
	// (setuid) cannot be killed by us anyway (ADR-105 butir 18).
	err := syscall.Kill(-u.p.Pid, sig)
	if err != nil && !errors.Is(err, syscall.ESRCH) && !errors.Is(err, syscall.EPERM) {
		return fmt.Errorf("exec: kill -%d: %w", u.p.Pid, err)
	}
	return nil
}

// wait reaps the leader (wait4 on its pid only: other os/exec users in the
// process keep their own children, ADR-105).
func (u *osProc) wait() (exit, error) {
	st, err := u.p.Wait()
	if err != nil {
		return exit{}, fmt.Errorf("exec: wait: %w", err)
	}
	ws, ok := st.Sys().(syscall.WaitStatus)
	if !ok {
		return exit{code: st.ExitCode()}, nil
	}
	if ws.Signaled() {
		return exit{code: -1, signal: signalName(ws.Signal())}, nil
	}
	return exit{code: ws.ExitStatus()}, nil
}

func signalName(s syscall.Signal) string {
	if n := unix.SignalName(s); n != "" {
		return n
	}
	return "SIG" + strconv.Itoa(int(s))
}

func closeAll(fs ...*os.File) error {
	var err error
	for _, f := range fs {
		err = errors.Join(err, f.Close())
	}
	return err
}
