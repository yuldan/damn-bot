//go:build unix

// Transport isolation for `damnbot acp` (GAP-REL-13, SPEC §13.3).
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// isolateACPStdio duplicates the protocol stdin/stdout to private
// close-on-exec descriptors for the JSON-RPC transport, then points fd 0 at
// /dev/null and fd 1 at <data_dir>/logs/acp.log. A child spawned by the
// session (shell, MCP stdio, LSP, hook, execute_code, git) therefore cannot
// inherit — or flip O_NONBLOCK on — the protocol pipe, even if it inherits
// the process's standard descriptors.
func isolateACPStdio(dataDir string) (in, out, logf *os.File, err error) {
	in, err = dupPrivate(0, "acp-stdin")
	if err != nil {
		return nil, nil, nil, err
	}
	out, err = dupPrivate(1, "acp-stdout")
	if err != nil {
		return nil, nil, nil, errors.Join(err, in.Close())
	}
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, nil, nil, errors.Join(fmt.Errorf("acp: open %s: %w", os.DevNull, err), in.Close(), out.Close())
	}
	defer func() { err = errors.Join(err, null.Close()) }()
	if derr := unix.Dup2(int(null.Fd()), 0); derr != nil {
		return nil, nil, nil, errors.Join(fmt.Errorf("acp: redirect stdin: %w", derr), in.Close(), out.Close())
	}
	absDir, aerr := filepath.Abs(dataDir)
	if aerr != nil {
		return nil, nil, nil, errors.Join(fmt.Errorf("acp: data dir: %w", aerr), in.Close(), out.Close())
	}
	// The path is resolved by config.Loader from the profile, never a request.
	if merr := os.MkdirAll(absDir, 0o700); merr != nil { //nolint:gosec // G703 false positive: config-derived data dir
		return nil, nil, nil, errors.Join(fmt.Errorf("acp: data dir: %w", merr), in.Close(), out.Close())
	}
	root, rerr := os.OpenRoot(absDir)
	if rerr != nil {
		return nil, nil, nil, errors.Join(fmt.Errorf("acp: data dir: %w", rerr), in.Close(), out.Close())
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	if merr := root.Mkdir("logs", 0o700); merr != nil && !errors.Is(merr, os.ErrExist) {
		return nil, nil, nil, errors.Join(fmt.Errorf("acp: log dir: %w", merr), in.Close(), out.Close())
	}
	logf, err = root.OpenFile("logs/acp.log", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, nil, errors.Join(fmt.Errorf("acp: open log: %w", err), in.Close(), out.Close())
	}
	if derr := unix.Dup2(int(logf.Fd()), 1); derr != nil {
		return nil, nil, nil, errors.Join(fmt.Errorf("acp: redirect stdout: %w", derr), in.Close(), out.Close(), logf.Close())
	}
	return in, out, logf, nil
}

// dupPrivate duplicates fd to a close-on-exec descriptor.
func dupPrivate(fd int, name string) (*os.File, error) {
	nfd, err := unix.Dup(fd)
	if err != nil {
		return nil, fmt.Errorf("acp: dup %d: %w", fd, err)
	}
	unix.CloseOnExec(nfd)
	f := os.NewFile(uintptr(nfd), name)
	if f == nil {
		return nil, errors.Join(fmt.Errorf("acp: wrap fd %d", nfd), unix.Close(nfd))
	}
	return f, nil
}
