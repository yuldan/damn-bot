//go:build unix

package exec

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// sessionShell is §A.2's session shell: $SHELL when bash or zsh, else
// bash, else sh, on a PTY with line editing off (`--noediting`, zsh `+Z`)
// so no bracketed-paste or redraw sequences frame the output. fish is not
// used: its line editor echoes and redraws input whatever the TTY mode
// (ADR-105 butir 22).
func sessionShell(path string) sessionShellSpec {
	c, err := chooseShell(path)
	bin := "/bin/sh"
	if err == nil {
		bin = c.bin
	}
	argv := []string{bin}
	switch filepath.Base(bin) {
	case "bash":
		argv = append(argv, "--norc", "--noprofile", "--noediting")
	case "zsh":
		argv = append(argv, "-f", "+Z")
	}
	const sentinel = `printf '\036DB:%s:%d:\036' `
	return sessionShellSpec{
		argv: argv, pty: true, ext: ".sh",
		setup: "stty -echo 2>/dev/null; PS1=''; PS2=''; PROMPT_COMMAND=''; " +
			"shopt -u expand_aliases 2>/dev/null; unsetopt aliases 2>/dev/null; unalias -a 2>/dev/null; true",
		line: func(path, cwd, nonce string) string {
			return ". " + shQuote(path) + "; __dmb_s=$?; pwd -P > " + shQuote(cwd) + "; " +
				sentinel + nonce + ` "$__dmb_s"` + "\n"
		},
		again: func(nonce, cwd string) string {
			return "pwd -P > " + shQuote(cwd) + "; " + sentinel + nonce + ` 130` + "\n"
		},
	}
}

// shQuote single-quotes p for POSIX shells.
func shQuote(p string) string { return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'" }

// interruptForeground sends SIGINT to the PTY's foreground process group.
func interruptForeground(s *session) error {
	raw, err := s.input.SyscallConn()
	if err != nil {
		return fmt.Errorf("pty: %w", err)
	}
	var kerr error
	cerr := raw.Control(func(fd uintptr) {
		pgrp, err := unix.IoctlGetInt(int(fd), unix.TIOCGPGRP)
		if err != nil || pgrp <= 0 {
			kerr = err
			return
		}
		if err := syscall.Kill(-pgrp, syscall.SIGINT); err != nil && !errors.Is(err, syscall.ESRCH) {
			kerr = err
		}
	})
	return errors.Join(cerr, kerr)
}
