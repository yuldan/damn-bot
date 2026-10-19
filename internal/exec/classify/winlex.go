package classify

import (
	"slices"
	"strings"
)

// winAliases maps PowerShell/cmd names to the POSIX command whose rules
// apply (name:posix).
const (
	winAliases = " remove-item:rm ri:rm del:rm erase:rm rd:rm rmdir:rm get-content:cat gc:cat type:cat" +
		" get-childitem:ls gci:ls dir:ls copy-item:cp copy:cp cpi:cp move-item:mv move:mv mi:mv set-content:tee" +
		" sc:tee add-content:tee ac:tee out-file:tee new-item:touch ni:touch select-string:grep sls:grep "
)

// winLine classifies a PowerShell or cmd.exe line with a token lexer, not
// a full parser (EXEC-AND-CODING §A.8): unrecognised structure ($(…),
// script blocks, parentheses) is Unknown, and every simple command found
// is still labeled.
func (s *state) winLine(src string, d Dialect) {
	if s.depth >= maxDepth {
		s.raise(Unknown, "nesting too deep")
		return
	}
	s.depth++
	saved := s.fold
	s.fold = true
	lx := lexer{s: s, src: src, d: d}
	for lx.i < len(lx.src) {
		lx.i++
		lx.byte(lx.src[lx.i-1])
	}
	lx.flush()
	lx.end(false)
	s.fold = saved
	s.depth--
}

// lexer accumulates tokens of the current simple command.
type lexer struct {
	s     *state
	src   string
	d     Dialect
	i     int
	cur   strings.Builder
	tok   arg  // flags of the token being built
	open  bool // a token is being built
	args  []arg
	redir byte // pending redirect operator
	in    stdin
}

func (lx *lexer) byte(c byte) {
	switch {
	case c == ' ' || c == '\t' || (lx.d == Cmd && (c == ';' || c == ',')):
		lx.flush()
	case c == '\r' || c == '\n' || (c == ';' && lx.d == Pwsh):
		lx.flush()
		lx.end(false)
	case c == '|' || c == '&':
		lx.operator(c)
	case c == '>' || c == '<':
		lx.redirect(c)
	default:
		lx.char(c)
	}
}

// char appends one byte of a word, handling quotes and expansions.
func (lx *lexer) char(c byte) {
	if !lx.open {
		lx.open, lx.tok = true, arg{lit: true}
	}
	switch {
	case c == '\x22' || (c == '\x27' && lx.d == Pwsh):
		lx.quoted(c)
	case (c == '$' || c == '%' || c == '!') && lx.variable(c):
	case (c == '\x60' && lx.d == Pwsh) || (c == '^' && lx.d == Cmd):
		lx.escape()
	default:
		lx.plain(c)
	}
}

// escape appends the next byte literally (PowerShell ` or cmd ^).
func (lx *lexer) escape() {
	if lx.i < len(lx.src) {
		lx.cur.WriteByte(lx.src[lx.i])
		lx.i++
	}
}

// plain appends an unquoted byte; grouping/subexpression syntax is not
// analysed (Unknown).
func (lx *lexer) plain(c byte) {
	if strings.IndexByte("(){}@", c) >= 0 {
		lx.tok.lit = false
		lx.s.raise(Unknown, "unrecognised PowerShell/cmd structure")
	}
	lx.tok.glob = lx.tok.glob || strings.IndexByte("*?[", c) >= 0
	lx.cur.WriteByte(c)
}

// quoted reads a quoted section; PowerShell double quotes expand $ and `.
func (lx *lexer) quoted(q byte) {
	lx.tok.quoted = true
	for lx.i < len(lx.src) {
		c := lx.src[lx.i]
		lx.i++
		switch {
		case c == q:
			return
		case q == '\x22' && (c == '$' || c == '%' || c == '!') && lx.variable(c):
		case q == '\x22' && c == '\x60' && lx.d == Pwsh:
			lx.tok.lit = false
		default:
			lx.cur.WriteByte(c)
		}
	}
	lx.tok.lit = false // unterminated quote
}

// variable handles $name / $env:NAME (PowerShell) and %NAME% / !NAME!
// (cmd). Home variables expand; secret references are flagged; anything
// else makes the token dynamic. It reports false for a literal sigil.
func (lx *lexer) variable(c byte) bool {
	if (c == '$') != (lx.d == Pwsh) {
		return false
	}
	j := lx.i
	for j < len(lx.src) && (isLetter(lx.src[j]) || isDigits(lx.src[j:j+1]) || lx.src[j] == '_' || lx.src[j] == ':') {
		j++
	}
	name := lx.src[lx.i:j]
	if c != '$' && (j >= len(lx.src) || lx.src[j] != c) {
		return false
	}
	lx.i = j + len(strings.TrimPrefix(string(c), "$"))
	bare := strings.TrimPrefix(strings.ToLower(name), "env:")
	if slices.ContainsFunc(lx.s.env.SecretVars, func(v string) bool { return strings.EqualFold(v, bare) }) {
		lx.s.flag(ReferencesSecret, "references secret "+name)
		lx.s.raise(Unknown, "references secret")
	}
	if bare == "home" || bare == "userprofile" {
		lx.cur.WriteString(lx.s.env.Home)
		return true
	}
	lx.tok.lit = false
	lx.cur.WriteString(string(c) + name)
	return true
}

func (lx *lexer) operator(c byte) {
	lx.flush()
	double := lx.i < len(lx.src) && lx.src[lx.i] == c
	if double {
		lx.i++
	}
	// A leading & in PowerShell is the call operator (& 'C:\tool.exe').
	if c == '|' || double || lx.d != Pwsh || len(lx.args) > 0 {
		lx.end(c == '|' && !double)
	}
}

func (lx *lexer) redirect(c byte) {
	lx.flush()
	if lx.i < len(lx.src) && lx.src[lx.i] == '>' {
		lx.i++
	}
	if lx.i+1 < len(lx.src) && lx.src[lx.i] == '&' && isDigits(lx.src[lx.i+1:lx.i+2]) {
		lx.i += 2 // 2>&1
		return
	}
	lx.redir = c
}

// flush finishes the current token (as a redirect target or an argument).
func (lx *lexer) flush() {
	if !lx.open {
		return
	}
	a := lx.tok
	a.s = lx.cur.String()
	lx.open = false
	lx.cur.Reset()
	switch lx.redir {
	case '>':
		lx.s.mut = true
		lx.s.operand(a, true)
	case '<':
		lx.s.operand(a, false)
	default:
		lx.args = append(lx.args, a)
	}
	lx.redir = 0
}

// end classifies the pending command; pipe marks the next one's stdin.
func (lx *lexer) end(pipe bool) {
	fetch := len(lx.args) > 0 && lx.s.command(lx.args, lx.in)
	lx.args, lx.in = nil, stdinNone
	if pipe && fetch {
		lx.in = stdinFetch
	} else if pipe {
		lx.in = stdinPipe
	}
}

// pwsh: -Command recurses with the PowerShell lexer, -EncodedCommand is
// Unknown, other options except -No* take a value.
func (s *state) pwsh(args []arg, in stdin) {
	for i := 0; i < len(args); i++ {
		switch l := strings.ToLower(args[i].s); {
		case l == "-e" || l == "-ec" || strings.HasPrefix(l, "-en"):
			s.raise(Unknown, "encoded PowerShell command")
			return
		case abbrev(l, "-command"):
			s.inline(args[i+1:], Pwsh)
			return
		case abbrev(l, "-file"):
			s.operand(arg{s: at(argStrings(args), i+1), lit: true}, false)
			return
		case strings.HasPrefix(l, "-no"):
		case strings.HasPrefix(l, "-"):
			i++
		default:
			s.inline(args[i:], Pwsh)
			return
		}
	}
	s.fromStdin("pwsh", in)
}

// cmdShell classifies `cmd [/flags] /c|/k command` by parsing the rest of
// the line with the Cmd lexer. An interactive cmd (no /c|/k) is Unknown
// (CL5-1): otherwise `cmd /c rd /s /q <root>` was labelled mutating and
// auto-approved while bare rd follows the rm rules.
func (s *state) cmdShell(args []arg, in stdin) {
	for i := 1; i < len(args); i++ {
		switch l := strings.ToLower(args[i].s); {
		case l == "/c" || l == "/k":
			rest := args[i+1:]
			if len(rest) == 0 {
				s.raise(Unknown, "cmd "+l+" without a command")
				return
			}
			s.inline(rest, Cmd)
			return
		case strings.HasPrefix(l, "/"):
			// /d, /q, /s, /v:on, /a, /u, ... carry no command.
		default:
			s.fromStdin("cmd", in)
			return
		}
	}
	s.fromStdin("cmd", in)
}

// cmdBuiltin recurses into cmd builtins that launch a command: start
// [options] [title] program and call program. A quoted title token is
// skipped, and a label call is Unknown; control flow (if/for/do/goto) is
// Unknown in command (CL5-9).
func (s *state) cmdBuiltin(name string, args []arg, in stdin) {
	rest := args[1:]
	if name == "start" {
		// start [options] [title] program: a quoted first token is the
		// window title, /d|/machine|/node|/affinity take a value, any other
		// option is Unknown (CL5-9).
	scan:
		for len(rest) > 0 {
			v := rest[0]
			switch {
			case v.s == "" || v.quoted:
				rest = rest[1:]
			case strings.HasPrefix(v.s, "/"):
				opt := strings.ToLower(v.s)
				rest = rest[1:]
				switch opt {
				case "/d", "/machine", "/node", "/affinity":
					if len(rest) == 0 {
						s.raise(Unknown, "start "+opt+" without a value")
						return
					}
					rest = rest[1:]
				case "/b", "/i", "/min", "/max", "/low", "/normal", "/high", "/realtime",
					"/abovenormal", "/belownormal", "/wait", "/separate", "/shared", "/restricted":
				default:
					s.raise(Unknown, "start option "+opt)
					return
				}
			default:
				break scan
			}
		}
	}
	if len(rest) == 0 {
		s.raise(Unknown, name+" without a command")
		return
	}
	if name == "call" && strings.HasPrefix(rest[0].s, ":") {
		s.raise(Unknown, "call label")
		return
	}
	s.nested(rest, in)
}

func isDigits(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}
