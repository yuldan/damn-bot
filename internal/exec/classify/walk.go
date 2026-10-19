package classify

import (
	"fmt"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// stdin describes where a command's standard input comes from; an
// interpreter reading its program from it is Unknown (ADR-069).
type stdin uint8

const (
	stdinNone  stdin = iota
	stdinPipe        // previous pipeline element
	stdinFetch       // previous element downloads (curl | sh)
	stdinDoc         // heredoc or here-string
)

// Classify labels cmd. It never fails: anything it cannot analyse is
// Unknown.
func Classify(cmd string, env *Env) Verdict {
	s := newState(env)
	if env.Dialect >= Pwsh {
		s.winLine(cmd, env.Dialect)
	} else {
		s.shell(cmd, env.Dialect == POSIX, stdinNone)
	}
	s.v.NonMutating = !s.mut && s.v.Class == Mutating && s.v.Flags == 0
	return s.v
}

// shell parses src as bash (or POSIX) and classifies every statement.
func (s *state) shell(src string, posix bool, in stdin) {
	if s.depth >= maxDepth {
		s.raise(Unknown, "nesting too deep")
		return
	}
	f, err := parse(src, posix)
	if err != nil {
		s.raise(Unknown, "parse error")
		return
	}
	s.depth++
	defer func() { s.depth-- }()
	syntax.Walk(f, func(n syntax.Node) bool {
		if pe, ok := n.(*syntax.ParamExp); ok && pe.Param != nil && slices.Contains(s.env.SecretVars, pe.Param.Value) {
			s.flag(ReferencesSecret, "references secret $"+pe.Param.Value)
			s.raise(Unknown, "references secret")
		}
		return true
	})
	w := walker{s: s, src: src}
	for _, st := range f.Stmts {
		w.stmt(st, in)
	}
}

type walker struct {
	s   *state
	src string
}

// visit classifies every statement and command substitution below n.
func (w *walker) visit(n syntax.Node) {
	syntax.Walk(n, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.Stmt:
			w.stmt(x, stdinNone)
			return false
		case *syntax.CmdSubst, *syntax.ProcSubst:
			w.s.mut = true
		}
		return true
	})
}

// stmt classifies one statement and reports whether its output is a
// download (for curl | sh).
func (w *walker) stmt(st *syntax.Stmt, in stdin) bool {
	for _, r := range st.Redirs {
		in = w.redirect(r, in)
	}
	switch x := st.Cmd.(type) {
	case nil:
		return false
	case *syntax.CallExpr:
		return w.call(x, in)
	case *syntax.BinaryCmd:
		if x.Op == syntax.Pipe || x.Op == syntax.PipeAll {
			next := stdinPipe
			if w.stmt(x.X, in) {
				next = stdinFetch
			}
			return w.stmt(x.Y, next)
		}
		w.stmt(x.X, in)
		return w.stmt(x.Y, in)
	default: // compound commands, functions, declarations: walk their contents
		if f, ok := x.(*syntax.FuncDecl); ok && f.Name != nil && calls(f.Body, f.Name.Value) {
			w.s.raise(Destructive, "recursive function (fork bomb)")
		}
		w.s.mut = true
		w.visit(x)
	}
	return false
}

// calls reports whether body invokes name.
func calls(body syntax.Node, name string) bool {
	found := false
	syntax.Walk(body, func(n syntax.Node) bool {
		if c, ok := n.(*syntax.CallExpr); ok && len(c.Args) > 0 && c.Args[0].Lit() == name {
			found = true
		}
		return !found
	})
	return found
}

// redirect classifies one redirection's target and returns the resulting
// stdin source.
func (w *walker) redirect(r *syntax.Redirect, in stdin) stdin {
	if r.Op == syntax.Hdoc || r.Op == syntax.DashHdoc || r.Op == syntax.WordHdoc {
		if r.Hdoc != nil {
			w.visit(r.Hdoc)
		}
		w.visit(r.Word)
		return stdinDoc
	}
	a := w.word(r.Word)
	if r.Op == syntax.RdrIn {
		w.s.operand(a, false)
		return stdinNone
	}
	w.s.mut = true
	w.s.operand(a, true)
	return in
}

// call classifies a simple command with its assignments.
func (w *walker) call(c *syntax.CallExpr, in stdin) bool {
	args := make([]arg, 0, len(c.Args))
	for _, a := range c.Assigns {
		w.s.mut = true
		w.visit(a)
		if a.Name != nil && len(c.Args) > 0 && riskyEnv(a.Name.Value) {
			w.s.raise(Unknown, "environment override "+a.Name.Value)
		}
	}
	for _, wd := range c.Args {
		args = append(args, w.word(wd))
	}
	if len(args) == 0 {
		return false
	}
	return w.s.command(args, in)
}

// riskyEnv names variables that change what runs or how it is resolved.
func riskyEnv(name string) bool {
	return inList(" PATH BASH_ENV ENV IFS NODE_OPTIONS PYTHONPATH PYTHONSTARTUP PERL5OPT RUBYOPT GIT_SSH_COMMAND GIT_EXEC_PATH PROMPT_COMMAND ", name) ||
		strings.HasPrefix(name, "LD_") || strings.HasPrefix(name, "DYLD_")
}

// word evaluates wd statically; command substitutions inside it are
// classified as commands of their own.
func (w *walker) word(wd *syntax.Word) arg {
	w.visit(wd)
	var b strings.Builder
	a := arg{lit: true}
	for i, p := range wd.Parts {
		switch x := p.(type) {
		case *syntax.Lit:
			a.lit = w.unquoted(x.Value, i == 0, &a, &b) && a.lit
		case *syntax.SglQuoted:
			a.lit = a.lit && !x.Dollar
			b.WriteString(x.Value)
		case *syntax.DblQuoted:
			a.lit = w.dquoted(x, &b) && a.lit
		case *syntax.ParamExp:
			a.lit = w.home(x, &b) && a.lit
		default:
			a.lit = false
		}
	}
	if !a.lit {
		return arg{s: w.src[wd.Pos().Offset():wd.End().Offset()]}
	}
	a.s = b.String()
	return a
}

// unquoted appends an unquoted literal: a leading ~ or ~/ expands to Home,
// ~user and brace expansion are dynamic, * ? [ mark a glob.
func (w *walker) unquoted(v string, first bool, a *arg, b *strings.Builder) bool {
	tildeUser := first && strings.HasPrefix(v, "~") && v != "~" && !strings.HasPrefix(v, "~/")
	brace := strings.Contains(v, "{") && (strings.Contains(v, ",") || strings.Contains(v, ".."))
	lit := !tildeUser && !brace
	if first && strings.HasPrefix(v, "~") {
		v = w.s.env.Home + v[1:]
	}
	a.glob = a.glob || strings.ContainsAny(v, "*?[")
	b.WriteString(unescape(v, false))
	return lit
}

func (w *walker) dquoted(q *syntax.DblQuoted, b *strings.Builder) bool {
	if q.Dollar {
		return false
	}
	for _, p := range q.Parts {
		switch x := p.(type) {
		case *syntax.Lit:
			b.WriteString(unescape(x.Value, true))
		case *syntax.ParamExp:
			if !w.home(x, b) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// home expands exactly $HOME / ${HOME}; any other parameter is dynamic.
func (w *walker) home(p *syntax.ParamExp, b *strings.Builder) bool {
	src := w.src[p.Pos().Offset():p.End().Offset()]
	if src != "$HOME" && src != "${HOME}" {
		return false
	}
	b.WriteString(w.s.env.Home)
	return true
}

// unescape removes backslash escapes and line continuations; dq limits
// escapes to those valid inside double quotes (\$ \` \" \\ \newline).
func unescape(v string, dq bool) string {
	var b strings.Builder
	for i := 0; i < len(v); i++ {
		if v[i] == '\\' && i+1 < len(v) && (!dq || strings.IndexByte("$\x60\"\\\n", v[i+1]) >= 0) {
			i++
			if v[i] == '\n' {
				continue
			}
		}
		b.WriteByte(v[i])
	}
	return b.String()
}

// parse parses src as bash, or POSIX sh.
func parse(src string, posix bool) (*syntax.File, error) {
	lang := syntax.LangBash
	if posix {
		lang = syntax.LangPOSIX
	}
	f, err := syntax.NewParser(syntax.Variant(lang)).Parse(strings.NewReader(src), "")
	if err != nil {
		return nil, fmt.Errorf("classify: %w", err)
	}
	return f, nil
}
