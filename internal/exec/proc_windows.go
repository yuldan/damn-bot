package exec

import (
	"errors"
	"fmt"
	"math"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// osProc is one process tree inside a Job Object with
// KILL_ON_JOB_CLOSE: the child starts suspended, joins the job, then
// resumes, so no descendant can escape before assignment (§A.4).
type osProc struct {
	p   *os.Process
	out *os.File
	job windows.Handle
	// ConPTY runner only (conpty_windows.go): the pseudo console and the
	// write end of its input pipe when no stdin was given (kept open so
	// the console reads block like a terminal).
	console, consoleIn windows.Handle
}

// basicAccounting mirrors JOBOBJECT_BASIC_ACCOUNTING_INFORMATION.
type basicAccounting struct {
	TotalUserTime, TotalKernelTime                       int64
	ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime   int64
	TotalPageFaultCount, TotalProcesses, ActiveProcesses uint32
	TotalTerminatedProcesses                             uint32
}

func start(s *Spec, in *os.File) (*osProc, error) {
	if s.PTY {
		return startConPTY(s, in)
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("exec: pipe: %w", err)
	}
	stdin, ownStdin := in, false
	if stdin == nil {
		if stdin, err = os.Open(os.DevNull); err != nil {
			return nil, errors.Join(fmt.Errorf("exec: open %s: %w", os.DevNull, err), closeAll(r, w))
		}
		ownStdin = true
	}
	job, err := newJob()
	if err != nil {
		return nil, errors.Join(err, closeAll(r, w), closeIf(ownStdin, stdin))
	}
	attr := &os.ProcAttr{
		Dir:   s.Dir,
		Env:   s.Env,
		Files: []*os.File{stdin, w, w},
		Sys: &syscall.SysProcAttr{
			CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP,
			HideWindow:    true,
		},
	}
	p, err := os.StartProcess(s.Argv[0], s.Argv, attr)
	cerr := errors.Join(closeAll(w), closeIf(ownStdin, stdin))
	if err != nil {
		return nil, errors.Join(fmt.Errorf("exec: start: %w", err), cerr, closeAll(r), windows.CloseHandle(job))
	}
	wp := &osProc{p: p, out: r, job: job}
	if aerr := errors.Join(cerr, wp.attach()); aerr != nil {
		// Suspended, outside any job: only the leader exists. Kill it
		// before Wait; a suspended process never exits on its own (EX39-2).
		werr := p.Kill()
		_, werr2 := p.Wait()
		return nil, errors.Join(aerr, werr, werr2, wp.release(), closeAll(r))
	}
	return wp, nil
}

func closeIf(own bool, f *os.File) error {
	if !own {
		return nil
	}
	return closeAll(f)
}

func newJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("exec: job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return 0, errors.Join(fmt.Errorf("exec: job limits: %w", err), windows.CloseHandle(job))
	}
	return job, nil
}

// attach assigns the suspended leader to the job and resumes it.
func (w *osProc) attach() (err error) {
	pid, err := pidU32(w.p.Pid)
	if err != nil {
		return err
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|
		windows.PROCESS_SUSPEND_RESUME, false, pid)
	if err != nil {
		return fmt.Errorf("exec: open process: %w", err)
	}
	defer func() { err = errors.Join(err, windows.CloseHandle(h)) }()
	if err := windows.AssignProcessToJobObject(w.job, h); err != nil {
		return fmt.Errorf("exec: assign job: %w", err)
	}
	// NtResumeProcess resumes every thread of the suspended process; the
	// Call error is the last-error value, meaningful only on failure status.
	st, _, callErr := windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess").Call(uintptr(h))
	if st != 0 {
		return fmt.Errorf("exec: resume: status %#x: %w", st, callErr)
	}
	return nil
}

func pidU32(pid int) (uint32, error) {
	if pid < 0 || pid > math.MaxUint32 {
		return 0, fmt.Errorf("exec: pid %d out of range", pid)
	}
	return uint32(pid), nil
}

func (w *osProc) output() *os.File   { return w.out }
func (w *osProc) closeOutput() error { return closeAll(w.out) }

// terminate: Windows has no SIGTERM for console trees; the job is
// terminated at once (kill_grace does not apply, §A.4).
func (w *osProc) terminate() error { return w.kill() }

func (w *osProc) kill() error {
	if err := windows.TerminateJobObject(w.job, 1); err != nil {
		return fmt.Errorf("exec: terminate job: %w", err)
	}
	return nil
}

func (w *osProc) treeAlive() bool {
	var info basicAccounting
	err := windows.QueryInformationJobObject(w.job, windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
	return err != nil || info.ActiveProcesses > 0
}

// release closes the job handle; KILL_ON_JOB_CLOSE kills any survivor.
func (w *osProc) release() error {
	err := w.closeConsole()
	if cerr := windows.CloseHandle(w.job); cerr != nil {
		err = errors.Join(err, fmt.Errorf("exec: close job: %w", cerr))
	}
	return err
}

func (w *osProc) wait() (exit, error) {
	st, err := w.p.Wait()
	if err != nil {
		return exit{}, fmt.Errorf("exec: wait: %w", err)
	}
	return exit{code: st.ExitCode()}, nil
}

func closeAll(fs ...*os.File) error {
	var err error
	for _, f := range fs {
		err = errors.Join(err, f.Close())
	}
	return err
}
