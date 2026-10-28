//go:build windows

// Transport isolation for `damnbot acp` (GAP-REL-13, SPEC §13.3).
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// isolateACPStdio duplicates STD_INPUT_HANDLE/STD_OUTPUT_HANDLE into
// non-inheritable handles for the JSON-RPC transport, then replaces the
// process standard handles with NUL and <data_dir>/logs/acp.log so no child
// (shell, MCP stdio, LSP, hook, execute_code, git) can inherit the protocol
// pipe. The Go os.Stdin/os.Stdout globals are replaced as well, so code that
// forwards them explicitly forwards the safe handles.
func isolateACPStdio(dataDir string) (in, out, logf *os.File, err error) {
	cur, err := windows.GetCurrentProcess()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("acp: current process: %w", err)
	}
	inH, err := dupPrivateHandle(cur, windows.STD_INPUT_HANDLE)
	if err != nil {
		return nil, nil, nil, err
	}
	outH, err := dupPrivateHandle(cur, windows.STD_OUTPUT_HANDLE)
	if err != nil {
		return nil, nil, nil, errors.Join(err, windows.CloseHandle(inH))
	}
	in = os.NewFile(uintptr(inH), "acp-stdin")
	out = os.NewFile(uintptr(outH), "acp-stdout")
	if in == nil || out == nil {
		return nil, nil, nil, errors.Join(errors.New("acp: wrap std handles"), windows.CloseHandle(inH), windows.CloseHandle(outH))
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, nil, nil, errors.Join(fmt.Errorf("acp: open %s: %w", os.DevNull, err), in.Close(), out.Close())
	}
	absDir, aerr := filepath.Abs(dataDir)
	if aerr != nil {
		return nil, nil, nil, errors.Join(fmt.Errorf("acp: data dir: %w", aerr), in.Close(), out.Close())
	}
	// The path is resolved by config.Loader from the profile, never a request.
	if merr := os.MkdirAll(absDir, 0o700); merr != nil { //nolint:gosec // G703 false positive: config-derived data dir
		return nil, nil, nil, errors.Join(fmt.Errorf("acp: data dir: %w", merr), in.Close(), out.Close(), null.Close())
	}
	root, rerr := os.OpenRoot(absDir)
	if rerr != nil {
		return nil, nil, nil, errors.Join(fmt.Errorf("acp: data dir: %w", rerr), in.Close(), out.Close(), null.Close())
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	if merr := root.Mkdir("logs", 0o700); merr != nil && !errors.Is(merr, os.ErrExist) {
		return nil, nil, nil, errors.Join(fmt.Errorf("acp: log dir: %w", merr), in.Close(), out.Close(), null.Close())
	}
	logf, err = root.OpenFile("logs/acp.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, nil, errors.Join(fmt.Errorf("acp: open log: %w", err), in.Close(), out.Close(), null.Close())
	}
	if serr := windows.SetStdHandle(windows.STD_INPUT_HANDLE, windows.Handle(null.Fd())); serr != nil {
		return nil, nil, nil, errors.Join(fmt.Errorf("acp: redirect stdin: %w", serr), in.Close(), out.Close(), null.Close(), logf.Close())
	}
	if serr := windows.SetStdHandle(windows.STD_OUTPUT_HANDLE, windows.Handle(logf.Fd())); serr != nil {
		return nil, nil, nil, errors.Join(fmt.Errorf("acp: redirect stdout: %w", serr), in.Close(), out.Close(), null.Close(), logf.Close())
	}
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = null, logf
	// Close the original standard files now. Their handles stay open in the
	// duplicated transport; letting a finalizer close them later aborts a
	// pending ReadFile on the duplicate with ERROR_OPERATION_ABORTED.
	if cerr := oldIn.Close(); cerr != nil {
		return nil, nil, nil, errors.Join(fmt.Errorf("acp: close original stdin: %w", cerr), in.Close(), out.Close(), null.Close(), logf.Close())
	}
	if cerr := oldOut.Close(); cerr != nil {
		return nil, nil, nil, errors.Join(fmt.Errorf("acp: close original stdout: %w", cerr), in.Close(), out.Close(), null.Close(), logf.Close())
	}
	return in, out, logf, nil
}

// dupPrivateHandle duplicates a standard handle without inheritance.
func dupPrivateHandle(cur windows.Handle, std uint32) (windows.Handle, error) {
	src, err := windows.GetStdHandle(std)
	if err != nil {
		return 0, fmt.Errorf("acp: get std handle: %w", err)
	}
	var dst windows.Handle
	if err := windows.DuplicateHandle(cur, src, cur, &dst, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
		return 0, fmt.Errorf("acp: duplicate std handle: %w", err)
	}
	return dst, nil
}
