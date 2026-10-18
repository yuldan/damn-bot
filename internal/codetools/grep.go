package codetools

import (
	"context"
	"os"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/damnbot/damnbot/internal/sandbox"
)

// scanOut is the per-file scan result produced on a walker worker.
type scanOut struct{}

// globMatch applies a doublestar pattern; without '/' it matches the
// basename at any depth (CODE-TOOLS §3).
func globMatch(pattern, rel, base string) bool {
	if !strings.Contains(pattern, "/") {
		ok, err := doublestar.Match(pattern, base)
		return ok && err == nil
	}
	ok, err := doublestar.Match(pattern, rel)
	return ok && err == nil
}

// searchRoot is a sandbox-checked grep/glob starting point.
type searchRoot struct {
	abs, rel string
	dir      bool
	outside  bool
	st       ignoreStack
	top      string
}

func (r *searchRoot) join(rel string) string {
	if r.rel == "." {
		return rel
	}
	return r.rel + "/" + rel
}

func (e *Engine) searchRoot(ctx context.Context, p string, o Opts) (*searchRoot, error) {
	if p == "" {
		p = "."
	}
	v, resolved, err := e.sb.Check(ctx, e.abs(p), sandbox.OpRead)
	if err != nil {
		return nil, mapOpenErr(p, err)
	}
	if aerr := approval(v, p, o.Approved); aerr != nil {
		return nil, aerr
	}
	fi, err := os.Lstat(resolved)
	if err != nil {
		return nil, mapOpenErr(p, err)
	}
	r := &searchRoot{abs: resolved, rel: e.rel(resolved), dir: fi.IsDir(), outside: v != sandbox.Inside}
	if r.dir {
		root := resolved
		for _, s := range e.roots {
			if hasPrefixPath(resolved, s, e.fold) {
				root = s
				break
			}
		}
		r.st, r.top = e.ignoreBase(ctx, root, resolved)
	}
	return r, nil
}
