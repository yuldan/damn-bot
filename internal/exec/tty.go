package exec

import (
	"path/filepath"
	"slices"
	"strings"
)

// NeedsTTY reports whether any simple command (classify Verdict.Argv)
// needs a terminal, selecting the PTY runner (§A.1): full-screen and pager
// programs, ssh without a remote command, REPLs without a script, `docker
// run|exec -t`, and terminal multiplexers. Everything else runs in a pipe.
func NeedsTTY(cmds [][]string) bool {
	for _, argv := range cmds {
		if len(argv) > 0 && needsTTY(argv) {
			return true
		}
	}
	return false
}

func needsTTY(argv []string) bool {
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(argv[0])), ".exe")
	operands := positional(argv[1:])
	switch name {
	case "top", "htop", "vim", "vi", "nano", "less", "more", "man", "script", "tmux", "screen":
		return true
	case "ssh":
		return len(operands) <= 1 // host only: interactive login
	case "python", "python3", "node", "irb":
		return len(operands) == 0 && !hasFlag(argv[1:], "-c", "-e", "-m", "--eval", "-p", "--print")
	case "docker", "podman":
		return len(operands) > 0 && (operands[0] == "run" || operands[0] == "exec") &&
			hasFlag(argv[1:], "-t", "-it", "-ti", "--tty")
	}
	return false
}

// positional returns the arguments that are not options. Option values
// are not tracked, so `ssh -p 22 host` counts "22" as an operand: the
// ambiguity resolves to pipe, which fails fast instead of hanging.
func positional(args []string) []string {
	var out []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-") || a == "-" {
			out = append(out, a)
		}
	}
	return out
}

func hasFlag(args []string, flags ...string) bool {
	return slices.ContainsFunc(args, func(a string) bool { return slices.Contains(flags, a) })
}
