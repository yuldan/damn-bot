// Package nohttpclient implements rule E-05 (ENGINEERING §5.1): only
// internal/netx may construct HTTP clients, transports or raw dialers.
// Everything else must obtain network access through netx so that
// timeouts, proxies and egress policy are applied in a single place.
package nohttpclient

import (
	"go/ast"
	"go/types"
	"strings"

	"golang.org/x/tools/go/analysis"
)

const (
	modulePath = "github.com/damnbot/damnbot"
	netxPath   = modulePath + "/internal/netx"
)

// Analyzer reports E-05 violations in module packages outside netx.
//
//nolint:gochecknoglobals // exported *analysis.Analyzer var is the go/analysis convention
var Analyzer = &analysis.Analyzer{
	Name: "nohttpclient",
	Doc:  "E-05: only internal/netx may construct http.Client/Transport, use http.Default*, or dial via net",
	Run:  run,
}

func run(pass *analysis.Pass) (any, error) {
	if checked(pass.Pkg.Path()) {
		for _, file := range pass.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				inspect(pass, n)
				return true
			})
		}
	}
	return nil, nil //nolint:nilnil // analyzer has no result value
}

// checked reports whether pkgPath belongs to the module and is not netx.
// External test packages ("…/netx_test") share their package's verdict.
func checked(pkgPath string) bool {
	p := strings.TrimSuffix(pkgPath, "_test")
	if p != modulePath && !strings.HasPrefix(p, modulePath+"/") {
		return false
	}
	return p != netxPath && !strings.HasPrefix(p, netxPath+"/")
}

func inspect(pass *analysis.Pass, n ast.Node) {
	switch n := n.(type) {
	case *ast.CompositeLit:
		// Elided literals in []*T{{}} are recorded with type *T.
		t := pass.TypesInfo.TypeOf(n)
		if p, ok := t.(*types.Pointer); ok {
			t = p.Elem()
		}
		if name, ok := forbiddenType(t); ok {
			report(pass, n, "composite literal of "+name)
		}
	case *ast.CallExpr:
		if !isBuiltinNew(pass.TypesInfo, n) {
			return
		}
		if name, ok := forbiddenType(pass.TypesInfo.TypeOf(n.Args[0])); ok {
			report(pass, n, "new("+name+")")
		}
	case *ast.Ident:
		if name, ok := forbiddenObject(pass.TypesInfo.Uses[n]); ok {
			report(pass, n, "use of "+name)
		}
	}
}

func report(pass *analysis.Pass, n ast.Node, what string) {
	pass.Reportf(n.Pos(), "nohttpclient: E-05: %s outside internal/netx; obtain network access via netx", what)
}

// forbiddenType matches the named type itself (aliases resolved), not pointers.
// These are the types whose construction is reserved for netx.
func forbiddenType(t types.Type) (string, bool) {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return "", false
	}
	obj := named.Obj()
	switch obj.Pkg().Path() + "." + obj.Name() {
	case "net/http.Client", "net/http.Transport", "net.Dialer":
		return qualified(obj), true
	}
	return "", false
}

// forbiddenObject matches package-level vars and funcs reserved for netx.
// http.Get/Head/Post/PostForm are included because they use DefaultClient.
// Only package-level objects match, so methods sharing a name
// (e.g. (*http.Client).Get, (*net.Dialer).Dial) are not reported.
func forbiddenObject(obj types.Object) (string, bool) {
	if obj == nil || obj.Pkg() == nil || obj.Pkg().Scope().Lookup(obj.Name()) != obj {
		return "", false
	}
	switch obj.Pkg().Path() + "." + obj.Name() {
	case "net/http.DefaultClient", "net/http.DefaultTransport",
		"net/http.Get", "net/http.Head", "net/http.Post", "net/http.PostForm",
		"net.Dial", "net.DialTimeout", "net.DialTCP", "net.DialUDP", "net.DialUnix", "net.DialIP":
		return qualified(obj), true
	}
	return "", false
}

func qualified(obj types.Object) string {
	return obj.Pkg().Name() + "." + obj.Name()
}

func isBuiltinNew(info *types.Info, call *ast.CallExpr) bool {
	id, ok := ast.Unparen(call.Fun).(*ast.Ident)
	if !ok || len(call.Args) != 1 {
		return false
	}
	b, ok := info.Uses[id].(*types.Builtin)
	return ok && b.Name() == "new"
}
