// Package nostdininherit rejects spawn sites that hand the process standard
// input to a child (GAP-REL-13, SPEC §13.3). The ACP/MCP transport owns a
// private stdin/stdout pair; children must get an explicit pipe or
// os.DevNull instead of os.Stdin, or they can consume protocol frames (or
// flip O_NONBLOCK on the transport).
package nostdininherit

import (
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// Analyzer reports os.Stdin in os.ProcAttr.Files and os/exec.Cmd.Stdin.
//
//nolint:gochecknoglobals // exported *analysis.Analyzer is the go/analysis convention
var Analyzer = &analysis.Analyzer{
	Name:     "nostdininherit",
	Doc:      "forbid os.Stdin in os.ProcAttr.Files and exec.Cmd.Stdin",
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

func run(pass *analysis.Pass) (any, error) {
	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)
	insp.Preorder([]ast.Node{(*ast.CompositeLit)(nil)}, func(n ast.Node) {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return
		}
		switch typeName(pass.TypesInfo.TypeOf(lit)) {
		case "os.ProcAttr":
			checkField(pass, lit, "Files")
		case "os/exec.Cmd":
			checkField(pass, lit, "Stdin")
		}
	})
	return nil, nil //nolint:nilnil // analyzer has no result value
}

// checkField inspects one composite-literal field for os.Stdin.
func checkField(pass *analysis.Pass, lit *ast.CompositeLit, field string) {
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok || identName(kv.Key) != field {
			continue
		}
		ast.Inspect(kv.Value, func(n ast.Node) bool {
			if isOsStdin(pass, n) {
				pass.Reportf(n.Pos(), "child must not inherit os.Stdin; use an explicit pipe or os.DevNull")
			}
			return true
		})
	}
}

// isOsStdin reports whether n is the selector os.Stdin.
func isOsStdin(pass *analysis.Pass, n ast.Node) bool {
	sel, ok := n.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Stdin" {
		return false
	}
	obj := pass.TypesInfo.ObjectOf(sel.Sel)
	v, ok := obj.(*types.Var)
	return ok && v.Pkg() != nil && v.Pkg().Path() == "os"
}

// identName returns an identifier or selector name.
func identName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

// typeName renders the package-qualified name of a type.
func typeName(t types.Type) string {
	named, ok := t.(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
		return ""
	}
	return named.Obj().Pkg().Path() + "." + named.Obj().Name()
}
