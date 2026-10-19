package classify

import (
	"path"
	"slices"
	"strings"
)

// File commands: every positional argument is an operand (EXEC-AND-CODING
// §A.8); patternCmds take a pattern first, copyCmds write their last one.
const (
	readCmds = " cat less more head tail wc nl od xxd hexdump strings file stat ls tree du md5sum sha1sum" +
		" sha256sum sha512sum base64 diff cmp cut tac realpath readlink grep egrep fgrep rg ag jq awk gawk sed "
	patternCmds = " grep egrep fgrep rg ag jq awk gawk sed "
	writeCmds   = " rm rmdir unlink mv touch mkdir tee truncate shred chmod chown chgrp "
	copyCmds    = " cp ln install "
)

// command classifies one simple command; it reports whether the command
// downloads (its output feeding an interpreter is curl | sh).
func (s *state) command(args []arg, in stdin) bool {
	s.v.Argv = append(s.v.Argv, argStrings(args))
	if !args[0].lit || args[0].glob {
		s.raise(Unknown, "dynamic command name "+args[0].s)
		s.mut = true
		return false
	}
	name := s.cmdName(args[0].s)
	if name == "git" {
		args = s.gitGlobals(args)
	}
	pos := s.positional(args[1:])
	sub := at(pos, 0)
	keys := [3]string{name, name + ":" + sub, name + ":" + sub + ":" + at(pos, 1)}
	s.mut = s.mut || !hasKey(nonMutatingKeys, keys)
	s.tables(name, keys, at(argStrings(args), 1))
	s.flagRules(name, sub, args[1:])
	switch {
	case name == "eval" || name == "iex" || name == "invoke-expression":
		s.eval(args[1:], in)
	case inList(privileged, name):
		s.privileged(name, args)
	case name == "cmd":
		s.cmdShell(args, in)
	case name == "start" || name == "call":
		s.cmdBuiltin(name, args, in)
	case name == "if" || name == "for" || name == "do" || name == "else" || name == "goto":
		s.raise(Unknown, "cmd control flow")
	case inList(wrappers, name):
		s.wrapper(name, args, in)
	case inList(interpreters, name) || strings.HasPrefix(name, "python"):
		s.interpreter(name, args, in)
	case inList(httpClients, name):
		s.http(args)
	case inList(remoteCmds, name):
		s.remoteCmd(name, args)
	default:
		s.tool(name, sub, args)
	}
	return inList(fetchers, name)
}

// cmdName is the lookup key: base name; on Windows dialects lower-cased,
// without .exe, PowerShell/cmd aliases mapped to their POSIX equivalent.
func (s *state) cmdName(a0 string) string {
	name := path.Base(s.clean(a0))
	if !s.fold {
		return name
	}
	name = strings.TrimSuffix(strings.ToLower(name), ".exe")
	if i := strings.Index(winAliases, " "+name+":"); i >= 0 && !strings.Contains(name, " ") {
		name, _, _ = strings.Cut(winAliases[i+len(name)+2:], " ")
	}
	return name
}

// operands checks file operands: every positional argument of known file
// commands, path-like arguments of everything else.
func (s *state) operands(name string, args []arg) {
	isFile := inList(readCmds+writeCmds+copyCmds, name)
	skip := 0
	if (inList(patternCmds, name) && !hasArg(args, "-e", "-f", "--regexp", "--file")) || inList(" chmod chown chgrp ", name) {
		skip = 1
	}
	last, pos := len(s.positional(args)), 0
	for _, a := range args {
		if s.optionOperand(a) || s.isOption(a.s) {
			continue
		}
		pos++
		write := inList(writeCmds, name) || (inList(copyCmds, name) && pos == last) || (name == "sed" && hasPrefixArg(args, "-i"))
		switch {
		case pos <= skip:
		case isFile:
			s.recursiveCheck(name, args, s.operand(a, write))
		case !a.lit || pathLike(a.s):
			s.operand(a, false)
		}
	}
}

// optionOperand checks the value of --opt=value when it is path-like or
// the option names a file list ("-" = list from stdin: Unknown).
func (s *state) optionOperand(a arg) bool {
	k, v, ok := strings.Cut(a.s, "=")
	fileList := inList(" --file --ignore-file --exclude-from --files-from --files0-from --from-file ", k)
	if !ok || !strings.HasPrefix(k, "-") || (!pathLike(v) && !fileList) {
		return false
	}
	if v == "-" {
		s.raise(Unknown, "file list from stdin: "+a.s)
	}
	s.operand(arg{s: v, lit: a.lit, glob: a.glob}, false)
	return true
}

// recursiveCheck: rm -r of the root itself or of .git is destructive.
func (s *state) recursiveCheck(name string, args []arg, paths []string) {
	if name != "rm" || !s.recursive(args) {
		return
	}
	for _, p := range paths {
		if path.Base(p) == ".git" || s.isRoot(p) {
			s.raise(Destructive, "recursive delete of "+p)
		}
	}
}

func (s *state) recursive(args []arg) bool {
	return slices.ContainsFunc(args, func(a arg) bool {
		l := strings.ToLower(a.s)
		if s.fold {
			return l == "/s" || abbrev(l, "-recurse")
		}
		return l == "--recursive" || (len(l) > 1 && l[0] == '-' && l[1] != '-' && strings.Contains(l, "r"))
	})
}

// abbrev reports whether l is a PowerShell parameter abbreviation of word.
func abbrev(l, word string) bool { return len(l) > 1 && strings.HasPrefix(word, l) }

func (s *state) isOption(v string) bool {
	return (strings.HasPrefix(v, "-") && v != "-") || (s.fold && strings.HasPrefix(v, "/") && len(v) <= 3)
}

func pathLike(v string) bool {
	return !strings.Contains(v, "://") && (strings.HasPrefix(v, "~") || strings.ContainsAny(v, `/\`) ||
		(len(v) >= 2 && v[1] == ':' && isLetter(v[0])))
}

// positional returns the non-option words.
func (s *state) positional(args []arg) []string {
	var out []string
	for _, a := range args {
		if !s.isOption(a.s) {
			out = append(out, a.s)
		}
	}
	return out
}

func at(v []string, i int) string {
	if i < len(v) {
		return v[i]
	}
	return ""
}

func argStrings(args []arg) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = a.s
	}
	return out
}

func hasArg(args []arg, vs ...string) bool {
	return slices.ContainsFunc(args, func(a arg) bool { return slices.Contains(vs, a.s) })
}

func hasPrefixArg(args []arg, p string) bool {
	return slices.ContainsFunc(args, func(a arg) bool { return strings.HasPrefix(a.s, p) })
}
