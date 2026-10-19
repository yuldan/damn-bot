package classify

import (
	"slices"
	"strings"
)

const (
	// wrapperValued lists wrapper options that consume the next word.
	wrapperValued = " sudo:-u sudo:-g sudo:-C sudo:-D sudo:-h sudo:-p sudo:-r sudo:-t sudo:-U sudo:-T doas:-u" +
		" doas:-C pkexec:--user timeout:-s timeout:-k nice:-n stdbuf:-i stdbuf:-o stdbuf:-e time:-f time:-o" +
		" xargs:-I xargs:-n xargs:-L xargs:-P xargs:-s xargs:-d xargs:-E xargs:-a npx:-p npx:--package env:-u" +
		" ssh:-p ssh:-i ssh:-o ssh:-l ssh:-F ssh:-J ssh:-L ssh:-R ssh:-D ssh:-b ssh:-c ssh:-E ssh:-e ssh:-m ssh:-S ssh:-w "
	// inlineFlags start inline program text for non-shell interpreters.
	inlineFlags  = " -c -e -E -p -r --eval --print --command "
	interpreters = " sh bash zsh dash ash hush ksh fish pwsh powershell node deno bun perl ruby php lua osascript "
	privileged   = " sudo doas pkexec su runas "
	wrappers     = " env nice nohup timeout stdbuf time command builtin exec xargs npx bunx pnpx uvx pipx" +
		" setsid unshare systemd-run taskset busybox toybox "
)

// privileged flags sudo-like commands and classifies the elevated command
// (SEC-14: never auto, ask per command).
func (s *state) privileged(name string, args []arg) {
	s.flag(Privileged, "privileged: "+name)
	rest := skipOpts(name, args[1:])
	if i := slices.IndexFunc(args, func(a arg) bool { return a.s == "-c" }); name == "su" && i > 0 && i+1 < len(args) {
		s.inline(args[i+1:i+2], Bash)
		return
	}
	if name == "su" || name == "runas" || len(rest) == 0 {
		s.raise(Unknown, "privileged shell or opaque command")
		return
	}
	s.nested(rest, stdinNone)
}

// wrapper classifies the command run by env/nice/timeout/xargs/npx/...
func (s *state) wrapper(name string, args []arg, in stdin) {
	rest := skipOpts(name, args[1:])
	switch name {
	case "env":
		for len(rest) > 0 && strings.Contains(rest[0].s, "=") {
			if k, _, _ := strings.Cut(rest[0].s, "="); riskyEnv(k) {
				s.raise(Unknown, "environment override "+k)
			}
			rest = rest[1:]
		}
		if slices.ContainsFunc(args, func(a arg) bool { return inList(" -C -S --chdir --split-string ", strings.SplitN(a.s, "=", 2)[0]) }) {
			s.raise(Unknown, "env changes directory or splits a string")
		}
	case "timeout":
		rest = rest[min(1, len(rest)):]
	case "pipx":
		if at(argStrings(rest), 0) == "run" {
			rest = skipOpts(name, rest[1:])
		}
	case "xargs":
		in = stdinNone
		if len(rest) > 0 {
			rest = append(slices.Clone(rest), arg{s: "<stdin>"})
		}
	case "setsid":
		rest = s.launcherArgs(args[1:], " -c -f -w -V --ctty --fork --wait --version ", "", "")
	case "unshare":
		rest = s.launcherArgs(args[1:], unshareBool, unshareValued, "mpnuUifrcT")
	case "taskset":
		rest = s.tasksetArgs(args[1:])
	case "busybox", "toybox":
		rest = args[1:]
		if len(rest) == 0 || strings.HasPrefix(rest[0].s, "-") {
			s.raise(Unknown, name+" without an applet")
			return
		}
	case "systemd-run":
		rest = args[1:]
		if len(rest) > 0 && strings.HasPrefix(rest[0].s, "-") {
			if i := slices.IndexFunc(rest, func(a arg) bool { return a.s == "--" }); i >= 0 {
				rest = rest[i+1:]
			} else {
				s.raise(Unknown, "systemd-run options")
				return
			}
		}
	}
	if rest == nil {
		return
	}
	if inList(" npx bunx pnpx uvx pipx ", name) {
		s.flag(Network, "network: "+name)
	}
	if len(rest) > 0 {
		s.nested(rest, in)
	}
}

// Launchers (CL5-2): unknown options are Unknown, never silently skipped.
const (
	unshareBool = " -m --mount -p --pid -n --net -u --uts -i --ipc -U --user -f --fork -r --map-root-user" +
		" -c --cgroup -T --time --kill-child --mount-proc "
	unshareValued = " --map-user --map-group --setuid --setgid --wd --root --propagation "
)

// launcherArgs returns the command a launcher runs, or nil after raising
// Unknown for an option it cannot account for.
func (s *state) launcherArgs(args []arg, boolOpts, valued, shorts string) []arg {
	for len(args) > 0 {
		v := args[0].s
		if v == "--" {
			args = args[1:]
			break
		}
		if !strings.HasPrefix(v, "-") {
			break
		}
		switch {
		case inList(boolOpts, v):
			args = args[1:]
		case inList(valued, v):
			if len(args) < 2 {
				s.raise(Unknown, "launcher option without a value: "+v)
				return nil
			}
			args = args[2:]
		case len(v) > 2 && v[1] != '-' && strings.Trim(v[1:], shorts) == "":
			args = args[1:]
		default:
			s.raise(Unknown, "launcher option "+v)
			return nil
		}
	}
	if len(args) == 0 {
		s.raise(Unknown, "launcher without a command")
		return nil
	}
	return args
}

// tasksetArgs skips options and the CPU mask, then returns the command.
func (s *state) tasksetArgs(args []arg) []arg {
	list := false
	for len(args) > 0 && strings.HasPrefix(args[0].s, "-") {
		v := args[0].s
		switch {
		case v == "-p" || v == "--pid":
			s.raise(Unknown, "taskset -p operates on a pid")
			return nil
		case v == "-c" || v == "--cpu-list":
			list = true
			if len(args) < 2 {
				s.raise(Unknown, "taskset -c without a list")
				return nil
			}
			args = args[2:]
		case strings.HasPrefix(v, "--cpu-list="):
			list = true
			args = args[1:]
		case v == "-a" || v == "--all-tasks" || v == "-V" || v == "--version" || v == "--":
			args = args[1:]
		default:
			s.raise(Unknown, "taskset option "+v)
			return nil
		}
	}
	if len(args) == 0 {
		s.raise(Unknown, "taskset without a command")
		return nil
	}
	if !list { // the next positional is the CPU mask
		args = args[1:]
	}
	if len(args) == 0 {
		s.raise(Unknown, "taskset without a command")
		return nil
	}
	return args
}

// skipOpts drops the leading options of a wrapper (with their values).
func skipOpts(name string, args []arg) []arg {
	for i := 0; i < len(args); i++ {
		switch a := args[i].s; {
		case a == "--":
			return args[i+1:]
		case !strings.HasPrefix(a, "-"):
			return args[i:]
		case inList(wrapperValued, name+":"+a):
			i++
		}
	}
	return nil
}

// nested classifies a command run by another one.
func (s *state) nested(args []arg, in stdin) {
	if s.depth >= maxDepth {
		s.raise(Unknown, "nesting too deep")
		return
	}
	s.depth++
	s.command(args, in)
	s.depth--
}

// eval classifies eval/iex: literal strings are parsed, anything else is
// Unknown.
func (s *state) eval(args []arg, in stdin) {
	s.mut = true
	if len(args) == 0 {
		s.fromStdin("eval", in)
		return
	}
	s.inline(args, s.env.Dialect)
}

// inline parses literal args joined by spaces as a command line of d.
func (s *state) inline(args []arg, d Dialect) {
	if slices.ContainsFunc(args, func(a arg) bool { return !a.lit }) {
		s.raise(Unknown, "evaluates a non-literal string")
		return
	}
	if src := strings.Join(argStrings(args), " "); d >= Pwsh {
		s.winLine(src, d)
	} else {
		s.shell(src, d == POSIX, stdinNone)
	}
}

func (s *state) fromStdin(name string, in stdin) {
	if in == stdinFetch {
		s.raise(Destructive, "download piped to "+name)
	}
	s.raise(Unknown, name+" reads its program from stdin")
}

// interpreter: shells recurse into a literal -c string; a program from
// stdin, pipe or heredoc, or inline code of another language, is Unknown
// (ADR-069, ADR-106 butir 4).
func (s *state) interpreter(name string, args []arg, in stdin) {
	s.mut = true
	switch {
	case name == "pwsh" || name == "powershell":
		s.pwsh(args[1:], in)
	case inList(" sh bash zsh dash ash hush ksh ", name):
		s.shellProgram(name, args[1:], in)
	default:
		s.program(name, args[1:], in)
	}
}

// program handles python/node/perl/...: inline code is Unknown, a script
// file is an operand, -m pip is pip.
func (s *state) program(name string, args []arg, in stdin) {
	for i, a := range args {
		v := a.s
		switch {
		case v == "-":
			s.fromStdin(name, in)
			return
		case v == "-m" && at(argStrings(args), i+1) == "pip":
			s.pip(at(s.positional(args[i+2:]), 0), args[i+1:])
			return
		case v == "eval" || slices.ContainsFunc(strings.Fields(inlineFlags), func(f string) bool { return strings.HasPrefix(v, f) }):
			s.raise(Unknown, "inline "+name+" program")
			return
		case !strings.HasPrefix(v, "-"):
			s.operand(a, false)
			return
		}
	}
	s.fromStdin(name, in)
}

func (s *state) shellProgram(name string, args []arg, in stdin) {
	cmdMode := false
	for i := 0; i < len(args); i++ {
		switch v := args[i].s; {
		case inList(" -o +o -O +O --rcfile --init-file ", v):
			i++
		case v == "-s" || v == "-":
			i = len(args)
		case strings.HasPrefix(v, "-") && !strings.HasPrefix(v, "--") && strings.Contains(v, "c"):
			cmdMode = true
		case strings.HasPrefix(v, "-") || strings.HasPrefix(v, "+"):
		case cmdMode && inList(" sh dash ash hush ", name):
			s.inline(args[i:i+1], POSIX)
			return
		case cmdMode:
			s.inline(args[i:i+1], Bash)
			return
		default:
			s.operand(args[i], false)
			return
		}
	}
	s.fromStdin(name, in)
}
