package codeintel

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strconv"
	"strings"
)

// parseGo extracts package, definitions and imports with go/ast. Malformed
// source is an error for that file, never a panic into the session.
func parseGo(path string, src []byte) (File, error) {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return File{}, fmt.Errorf("codeintel: parse %s: %w", path, err)
	}
	var f File
	f.Path, f.Package = path, node.Name.Name
	f.Symbols = append(f.Symbols, Symbol{Kind: KindPackage, Name: node.Name.Name, Line: fset.Position(node.Name.Pos()).Line, Col: fset.Position(node.Name.Pos()).Column})
	for _, imp := range node.Imports {
		p, uerr := strconv.Unquote(imp.Path.Value)
		if uerr != nil {
			continue
		}
		f.Imports = append(f.Imports, p)
		f.Symbols = append(f.Symbols, Symbol{Kind: KindImport, Name: p, Line: fset.Position(imp.Pos()).Line, Col: fset.Position(imp.Pos()).Column})
	}
	collectDecls(fset, node, &f)
	sort.SliceStable(f.Symbols, func(i, j int) bool {
		if f.Symbols[i].Line != f.Symbols[j].Line {
			return f.Symbols[i].Line < f.Symbols[j].Line
		}
		return f.Symbols[i].Name < f.Symbols[j].Name
	})
	sort.Strings(f.Imports)
	return f, nil
}

// collectDecls appends function/method/type symbols from the AST.
func collectDecls(fset *token.FileSet, node *ast.File, f *File) {
	for _, decl := range node.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			pos := fset.Position(d.Name.Pos())
			sym := Symbol{Kind: KindFunc, Name: d.Name.Name, Line: pos.Line, Col: pos.Column}
			if d.Recv != nil && len(d.Recv.List) > 0 {
				sym.Kind = KindMethod
				sym.Receiver = receiverName(d.Recv.List[0].Type)
			}
			f.Symbols = append(f.Symbols, sym)
		case *ast.GenDecl:
			if d.Tok != token.TYPE {
				continue
			}
			for _, spec := range d.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				pos := fset.Position(ts.Name.Pos())
				f.Symbols = append(f.Symbols, Symbol{Kind: KindType, Name: ts.Name.Name, Line: pos.Line, Col: pos.Column})
			}
		}
	}
}

// receiverName is the method receiver type without pointer/type arguments.
func receiverName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return receiverName(e.X)
	case *ast.IndexExpr:
		return receiverName(e.X)
	case *ast.IndexListExpr:
		return receiverName(e.X)
	case *ast.Ident:
		return e.Name
	}
	return ""
}

// rank computes a personalized PageRank over importer -> imported files.
// Personalization: focus terms in path/symbol names, then files with more
// definitions as a weak prior. Deterministic (files sorted before ranking).
//
//nolint:gocognit,gocyclo // one ranking pass: edges, personalization, 20 iterations
func rank(m *Map, focus string) {
	n := len(m.Files)
	if n == 0 {
		return
	}
	edges := make([][]int, n)
	for i := range m.Files {
		for _, imp := range m.Files[i].Imports {
			for _, j := range importTargets(m.Files, imp) {
				if j != i {
					edges[i] = append(edges[i], j)
				}
			}
		}
	}
	terms := strings.Fields(strings.ToLower(focus))
	person := make([]float64, n)
	var sum float64
	for i := range m.Files {
		w := 1.0
		var lower strings.Builder
		lower.WriteString(strings.ToLower(m.Files[i].Path + " " + m.Files[i].Package))
		for _, s := range m.Files[i].Symbols {
			lower.WriteString(" " + strings.ToLower(s.Name))
		}
		for _, t := range terms {
			if strings.Contains(lower.String(), t) {
				w += 3
			}
		}
		w += float64(len(m.Files[i].Symbols)) / 100
		person[i] = w
		sum += w
	}
	for i := range person {
		person[i] /= sum
	}
	score := append([]float64(nil), person...)
	next := make([]float64, n)
	for range 20 {
		for i := range next {
			next[i] = (1 - 0.85) * person[i]
		}
		for i := range m.Files {
			if len(edges[i]) == 0 {
				// Dangling node distributes to personalization.
				for j := range next {
					next[j] += 0.85 * score[i] * person[j]
				}
				continue
			}
			share := 0.85 * score[i] / float64(len(edges[i]))
			for _, j := range edges[i] {
				next[j] += share
			}
		}
		score, next = next, score
	}
	for i := range m.Files {
		m.Files[i].score = score[i]
	}
	sort.SliceStable(m.Files, func(i, j int) bool {
		if m.Files[i].score != m.Files[j].score {
			return m.Files[i].score > m.Files[j].score
		}
		return m.Files[i].Path < m.Files[j].Path
	})
}

// importTargets maps an import path to every file whose directory suffix
// matches it (deterministic: files are sorted before ranking).
func importTargets(files []File, imp string) []int {
	imp = strings.TrimSuffix(imp, "/")
	var out []int
	for i := range files {
		dir := files[i].Path[:strings.LastIndex(files[i].Path, "/")+1]
		dir = strings.TrimSuffix(dir, "/")
		if dir != "" && (imp == dir || strings.HasSuffix(imp, "/"+dir)) {
			out = append(out, i)
		}
	}
	return out
}

// Render writes the repository map deterministically, stopping exactly at
// maxTokens (approximated as 4 bytes per token, conservative for o200k).
//
//nolint:gocognit // one render pass with an exact byte budget
func (m *Map) Render(maxTokens int) string {
	if maxTokens < 200 {
		maxTokens = 200
	}
	if maxTokens > 1000 {
		maxTokens = 1000
	}
	budget := maxTokens * 4
	var b bytes.Buffer
	write := func(line string) bool {
		if b.Len()+len(line) > budget {
			return false
		}
		b.WriteString(line)
		return true
	}
	for i := range m.Files {
		f := &m.Files[i]
		if !write(f.Path + "\n") {
			break
		}
		if f.Package != "" && !write("  package "+f.Package+"\n") {
			break
		}
		for _, s := range f.Symbols {
			if s.Kind == KindPackage || s.Kind == KindImport {
				continue
			}
			var line string
			if s.Kind == KindMethod {
				line = "  (" + s.Receiver + ") " + s.Name + ":" + strconv.Itoa(s.Line) + "\n"
			} else {
				line = "  " + string(s.Kind) + " " + s.Name + ":" + strconv.Itoa(s.Line) + "\n"
			}
			if !write(line) {
				return b.String()
			}
		}
	}
	return b.String()
}
