package codetools

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"regexp"
	"strings"
)

// outlineMax caps outline entries per page (CODE-TOOLS §4.6).
const outlineMax = 400

// reDecl is the heuristic declaration pattern for files without a parser
// (CODE-TOOLS §4.6): indent, keyword, name.
var reDecl = regexp.MustCompile(`^([ \t]*)(?:(?:export|pub(?:\([a-z]+\))?|public|private|protected|static|async|abstract|default)\s+)*(def|class|function|fn|func|struct|interface|enum|trait|impl|type|module)\s+([A-Za-z_$][\w$]*)`)

type symbol struct {
	start, end, depth int
	kind, name        string
}

// outline renders the symbol list of a text file (Go: go/ast; others:
// heuristic). off is the entry offset (1-based) for paging.
func (e *Engine) outline(_ context.Context, f *fileView, off int, o Opts) (Result, error) {
	if f.binary {
		return f.binaryResult(e), nil
	}
	raw, sum, err := e.loadAll(f)
	if err != nil {
		return Result{}, err
	}
	tag := sumTag(sum)
	if f.key.size > fullHashMax {
		tag = statTag(f.key)
	}
	e.remember(f.t.resolved, tag, &tagEntry{sum: sum, stat: f.key}, raw)
	ol := e.symbols(f, raw, sum)
	syms, engine, perrs := ol.syms, ol.engine, ol.perrs
	if off < 1 {
		off = 1
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s #%s · outline · %s · %d symbols", f.t.rel, tag, engine, len(syms))
	if perrs > 0 {
		fmt.Fprintf(&b, " · parse_errors=%d", perrs)
	}
	budget := o.pageChars() - 200
	shown := 0
	for _, s := range syms[min(off-1, len(syms)):] {
		row := fmt.Sprintf("\n%sL%d-%d %s %s", strings.Repeat("  ", s.depth), s.start, s.end, s.kind, s.name)
		if shown == outlineMax || shown > 0 && budget-len(row) < 0 {
			break
		}
		budget -= len(row)
		b.WriteString(row)
		shown++
	}
	meta := map[string]any{"tag": tag, "symbols": len(syms), "outline": engine}
	if next := off + shown; next <= len(syms) {
		fmt.Fprintf(&b, "\nnext: offset=%d", next)
		meta["next"] = next
	}
	return Result{Content: e.wrapOutside(f.t, b.String()), Meta: meta}, nil
}

// outlined is a parsed outline, cached per (file version, sha256) (§7).
type outlined struct {
	sum    [32]byte
	syms   []symbol
	engine string
	perrs  int
}

func (e *Engine) symbols(f *fileView, raw []byte, sum [32]byte) *outlined {
	e.mu.Lock()
	ol, ok := e.cache.outlines.get(f.key)
	e.mu.Unlock()
	if ok && ol.sum == sum {
		return ol
	}
	ol = &outlined{sum: sum, engine: "heuristic"}
	text := logical(raw, f.enc)
	if strings.HasSuffix(f.t.resolved, ".go") {
		ol.engine = "go/ast"
		ol.syms, ol.perrs = goSymbols(text)
	} else {
		ol.syms = heuristicSymbols(text)
	}
	size := 64
	for _, s := range ol.syms {
		size += 48 + len(s.name)
	}
	e.mu.Lock()
	e.cache.outlines.put(f.key, ol, size)
	e.mu.Unlock()
	return ol
}

// goSymbols lists types, funcs, methods, consts and vars of a Go file; a
// file with syntax errors yields the declarations that parsed.
func goSymbols(src []byte) ([]symbol, int) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, parser.SkipObjectResolution)
	perrs := 0
	if el, ok := errors.AsType[scanner.ErrorList](err); ok {
		perrs = len(el)
	} else if err != nil {
		perrs = 1
	}
	if file == nil {
		return nil, perrs
	}
	line := func(p token.Pos) int { return fset.Position(p).Line }
	var out []symbol
	for _, d := range file.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				name = "(" + recvType(d.Recv.List[0].Type) + ") " + name
			}
			out = append(out, symbol{start: line(d.Pos()), end: line(d.End()), kind: "func", name: name})
		case *ast.GenDecl:
			out = append(out, specSymbols(d, line)...)
		}
	}
	return out, perrs
}

// specSymbols lists the types, consts and vars of one declaration group.
func specSymbols(d *ast.GenDecl, line func(token.Pos) int) []symbol {
	var out []symbol
	for _, s := range d.Specs {
		switch s := s.(type) {
		case *ast.TypeSpec:
			out = append(out, symbol{start: line(s.Pos()), end: line(s.End()), kind: "type", name: s.Name.Name + " " + typeKind(s.Type)})
		case *ast.ValueSpec:
			for _, n := range s.Names {
				out = append(out, symbol{start: line(s.Pos()), end: line(s.End()), kind: d.Tok.String(), name: n.Name})
			}
		}
	}
	return out
}

func recvType(x ast.Expr) string {
	switch t := x.(type) {
	case *ast.StarExpr:
		return "*" + recvType(t.X)
	case *ast.IndexExpr:
		return recvType(t.X)
	case *ast.IndexListExpr:
		return recvType(t.X)
	case *ast.Ident:
		return t.Name
	}
	return "?"
}

func typeKind(x ast.Expr) string {
	switch x.(type) {
	case *ast.StructType:
		return "struct"
	case *ast.InterfaceType:
		return "interface"
	case *ast.FuncType:
		return "func"
	}
	return ""
}

// heuristicSymbols matches declaration keywords line by line; nesting
// follows indentation and a symbol ends before the next one at the same or
// a lower indentation.
func heuristicSymbols(text []byte) []symbol {
	lines := strings.Split(strings.TrimSuffix(string(text), "\n"), "\n")
	var out []symbol
	var indents []int
	for i, l := range lines {
		m := reDecl.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		ind := len(strings.ReplaceAll(m[1], "\t", "    "))
		out = append(out, symbol{start: i + 1, end: len(lines), kind: m[2], name: m[3]})
		indents = append(indents, ind)
	}
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if indents[j] <= indents[i] {
				out[i].end = max(out[i].start, out[j].start-1)
				break
			}
		}
		for j := i - 1; j >= 0; j-- {
			if indents[j] < indents[i] && out[j].end >= out[i].start {
				out[i].depth = out[j].depth + 1
				break
			}
		}
	}
	return out
}
