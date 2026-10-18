package exec

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// startConPTY starts s attached to a pseudo console (§A.1: ConPTY 120×40
// inside the call's Job Object). The console reads in (piped stdin), or a
// pipe kept open so reads block like a terminal; its output — VT text —
// is the call output. CreateProcess is used directly: os.StartProcess has
// no STARTUPINFOEX for PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE (ADR-105 butir 21).
func startConPTY(s *Spec, in *os.File) (_ *osProc, err error) {
	w := &osProc{}
	defer func() {
		if err != nil {
			err = errors.Join(err, w.closeConsole(), closeHandle(w.job))
		}
	}()
	inR, err := w.consoleInput(in)
	if err != nil {
		return nil, err
	}
	var outR, outW windows.Handle
	if err = windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
		return nil, errors.Join(fmt.Errorf("exec: pty pipe: %w", err), closeHandle(inR))
	}
	w.out = os.NewFile(uintptr(outR), "conpty")
	err = windows.CreatePseudoConsole(windows.Coord{X: ptyCols, Y: ptyRows}, inR, outW, 0, &w.console)
	// The console holds its own duplicates of both ends.
	err = errors.Join(err, closeHandle(inR), closeHandle(outW))
	if err != nil {
		return nil, errors.Join(fmt.Errorf("exec: pseudo console: %w", err), closeAll(w.out))
	}
	if w.job, err = newJob(); err != nil {
		return nil, errors.Join(err, closeAll(w.out))
	}
	if err = w.createProcess(s); err != nil {
		return nil, errors.Join(err, closeAll(w.out))
	}
	return w, nil
}

// consoleInput returns the read end the console reads keys from: the
// piped stdin, or a fresh pipe whose write end stays open in w.
func (w *osProc) consoleInput(in *os.File) (windows.Handle, error) {
	if in != nil {
		var dup windows.Handle
		self := windows.CurrentProcess()
		if err := windows.DuplicateHandle(self, windows.Handle(in.Fd()), self, &dup, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
			return 0, fmt.Errorf("exec: pty stdin: %w", err)
		}
		return dup, nil
	}
	var r windows.Handle
	if err := windows.CreatePipe(&r, &w.consoleIn, nil, 0); err != nil {
		return 0, fmt.Errorf("exec: pty input pipe: %w", err)
	}
	return r, nil
}

// createProcess starts s suspended with the pseudo console attribute, puts
// it in the job, then resumes it (no descendant can start outside the job).
func (w *osProc) createProcess(s *Spec) (err error) {
	attrs, aerr := windows.NewProcThreadAttributeList(1)
	if aerr != nil {
		return fmt.Errorf("exec: attribute list: %w", aerr)
	}
	defer attrs.Delete()
	// lpValue is the HPCON itself, not a pointer to it.
	if err = attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, *(*unsafe.Pointer)(unsafe.Pointer(&w.console)), unsafe.Sizeof(w.console)); err != nil {
		return fmt.Errorf("exec: pseudo console attribute: %w", err)
	}
	si := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(si))
	app, err := windows.UTF16PtrFromString(s.Argv[0])
	if err != nil {
		return fmt.Errorf("exec: argv0: %w", err)
	}
	cmdline, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(s.Argv))
	if err != nil {
		return fmt.Errorf("exec: command line: %w", err)
	}
	var dir *uint16
	if s.Dir != "" {
		if dir, err = windows.UTF16PtrFromString(s.Dir); err != nil {
			return fmt.Errorf("exec: dir: %w", err)
		}
	}
	env, err := envBlock(s.Env)
	if err != nil {
		return err
	}
	var pi windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT | windows.CREATE_SUSPENDED)
	if err = windows.CreateProcess(app, cmdline, nil, nil, false, flags, &env[0], dir, &si.StartupInfo, &pi); err != nil {
		return fmt.Errorf("exec: start: %w", err)
	}
	defer func() { err = errors.Join(err, closeHandle(pi.Thread), closeHandle(pi.Process)) }()
	if aerr := windows.AssignProcessToJobObject(w.job, pi.Process); aerr != nil {
		return errors.Join(fmt.Errorf("exec: assign job: %w", aerr), windows.TerminateProcess(pi.Process, 1))
	}
	if _, rerr := windows.ResumeThread(pi.Thread); rerr != nil {
		return errors.Join(fmt.Errorf("exec: resume: %w", rerr), windows.TerminateJobObject(w.job, 1))
	}
	// The pid cannot be reused while pi.Process is open.
	if w.p, err = os.FindProcess(int(pi.ProcessId)); err != nil {
		return errors.Join(fmt.Errorf("exec: find process: %w", err), windows.TerminateJobObject(w.job, 1))
	}
	return nil
}

// envBlock is a CREATE_UNICODE_ENVIRONMENT block: "K=V\0…\0\0".
func envBlock(env []string) ([]uint16, error) {
	var b []uint16
	for _, kv := range env {
		if strings.IndexByte(kv, 0) >= 0 {
			return nil, fmt.Errorf("%w: NUL in environment", ErrSpec)
		}
		b = append(append(b, utf16.Encode([]rune(kv))...), 0)
	}
	if len(b) == 0 {
		b = append(b, 0)
	}
	return append(b, 0), nil
}

// closeConsole closes the pseudo console (its conhost exits and the output
// pipe reaches EOF) and the kept input pipe. Called while the reader still
// drains the output, as ClosePseudoConsole may wait for it.
func (w *osProc) closeConsole() error {
	if w.console != 0 {
		windows.ClosePseudoConsole(w.console)
		w.console = 0
	}
	h := w.consoleIn
	w.consoleIn = 0
	return closeHandle(h)
}

func closeHandle(h windows.Handle) error {
	if h == 0 {
		return nil
	}
	if err := windows.CloseHandle(h); err != nil {
		return fmt.Errorf("exec: close handle: %w", err)
	}
	return nil
}
