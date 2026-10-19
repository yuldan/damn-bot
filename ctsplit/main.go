// ctsplit: decl/field-level dependency analysis and projection of a Go package
// into cumulative stages.
//
//	ctsplit analyze <pkgdir> <stages.txt>
//	ctsplit emit <pkgdir> <stages.txt> <k> <outdir>
//	ctsplit units <pkgdir>
//
// stages.txt: "stage NAME" lines, then root patterns (unit name, "file.go:*",
// "-name" to exclude explicitly, "!name" = never include (manual partial)).
package main

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

type unit struct {
	id         string // file:name
	name       string
	file       string
	start, end int // byte offsets (span incl. leading comments)
	test       bool
	field      bool
	parent     string // struct unit id for fields
	method     bool
	recv       string
	deps       map[string]bool
	order      int
}

var (
	units  = map[string]*unit{}
	byName = map[string][]*unit{}
	srcs   = map[string][]byte{}
	files  = map[string]*ast.File{}
	fsets  = map[string]*token.FileSet{}
	fileOrder []string
)

func lineOf(src []byte, off int) int { return strings.Count(string(src[:off]), "\n") }

func load(dir string) {
	for _, goos := range []string{"linux", "darwin", "windows", "freebsd"} {
		cfg := &packages.Config{Mode: packages.LoadAllSyntax, Dir: dir, Tests: true,
			BuildFlags: []string{"-tags=goolm"}, Env: append(os.Environ(), "GOOS="+goos)}
		pkgs, err := packages.Load(cfg, ".")
		if err != nil {
			panic(err)
		}
		var p *packages.Package
		for _, q := range pkgs {
			if strings.Contains(q.ID, ".test]") && !strings.HasSuffix(q.PkgPath, "_test") {
				p = q
			}
		}
		if p == nil {
			for _, q := range pkgs {
				if !strings.HasSuffix(q.PkgPath, "_test") && !strings.HasSuffix(q.ID, ".test") {
					p = q
				}
			}
		}
		for _, e := range p.Errors {
			fmt.Fprintln(os.Stderr, goos, e)
		}
		// register units
		for i, f := range p.Syntax {
			path := p.CompiledGoFiles[i]
			base := filepath.Base(path)
			if _, ok := files[base]; !ok {
				src, _ := os.ReadFile(path)
				srcs[base] = src
				files[base] = f
				fsets[base] = p.Fset
				fileOrder = append(fileOrder, base)
				register(base, f, p.Fset, src)
			}
		}
		// edges
		for i, f := range p.Syntax {
			base := filepath.Base(p.CompiledGoFiles[i])
			_ = f
			for id, obj := range p.TypesInfo.Uses {
				pf := p.Fset.File(id.Pos())
				if pf == nil || filepath.Base(pf.Name()) != base {
					continue
				}
				from := find(base, pf.Offset(id.Pos()))
				if from == nil {
					continue
				}
				switch o := obj.(type) {
				case *types.Func:
					obj = o.Origin()
				case *types.Var:
					obj = o.Origin()
				}
				if obj.Pkg() == nil || obj.Pkg() != p.Types {
					continue
				}
				tf := p.Fset.File(obj.Pos())
				if tf == nil {
					continue
				}
				to := find(filepath.Base(tf.Name()), tf.Offset(obj.Pos()))
				if to == nil || to == from {
					continue
				}
				if v, ok := obj.(*types.Var); ok && !v.IsField() && obj.Parent() != p.Types.Scope() {
					continue // local var
				}
				from.deps[to.id] = true
			}
		}
	}
}

// find innermost unit containing off in file.
func find(file string, off int) *unit {
	var best *unit
	for _, u := range units {
		if u.file == file && off >= u.start && off < u.end {
			if best == nil || u.field {
				best = u
			}
		}
	}
	return best
}

var ord int

func register(base string, f *ast.File, fset *token.FileSet, src []byte) {
	test := strings.HasSuffix(base, "_test.go")
	tf := fset.File(f.Pos())
	off := func(p token.Pos) int { return tf.Offset(p) }
	prevEnd := -1
	// start after import decls
	for _, d := range f.Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.IMPORT {
			prevEnd = off(g.End())
		}
	}
	if prevEnd < 0 {
		prevEnd = off(f.Name.End())
	}
	add := func(name string, s, e int, extra func(*unit)) *unit {
		ord++
		u := &unit{id: base + ":" + name, name: name, file: base, start: s, end: e, test: test, deps: map[string]bool{}, order: ord}
		if extra != nil {
			extra(u)
		}
		units[u.id] = u
		byName[name] = append(byName[name], u)
		return u
	}
	for _, d := range f.Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.IMPORT {
			continue
		}
		s := spanStart(src, prevEnd)
		e := off(d.End())
		// include trailing line comment
		for e < len(src) && src[e] != '\n' {
			e++
		}
		prevEnd = e
		switch d := d.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			var recv string
			if d.Recv != nil {
				t := d.Recv.List[0].Type
				if st, ok := t.(*ast.StarExpr); ok {
					t = st.X
				}
				if ix, ok := t.(*ast.IndexListExpr); ok {
					t = ix.X
				}
				if ix, ok := t.(*ast.IndexExpr); ok {
					t = ix.X
				}
				recv = t.(*ast.Ident).Name
				name = recv + "." + name
			}
			u := add(name, s, e, func(u *unit) { u.method = recv != ""; u.recv = recv })
			if recv != "" {
				for _, r := range byName[recv] {
					u.deps[r.id] = true
				}
			}
		case *ast.GenDecl:
			var names []string
			for _, sp := range d.Specs {
				switch sp := sp.(type) {
				case *ast.TypeSpec:
					names = append(names, sp.Name.Name)
				case *ast.ValueSpec:
					for _, n := range sp.Names {
						names = append(names, n.Name)
					}
				}
			}
			name := strings.Join(names, ",")
			u := add(name, s, e, nil)
			if len(d.Specs) == 1 {
				if ts, ok := d.Specs[0].(*ast.TypeSpec); ok {
					if st, ok := ts.Type.(*ast.StructType); ok && !test {
						for _, fl := range st.Fields.List {
							fs := off(fl.Pos())
							if fl.Doc != nil {
								fs = off(fl.Doc.Pos())
							}
							// line start
							for fs > 0 && src[fs-1] != '\n' {
								fs--
							}
							fe := off(fl.End())
							for fe < len(src) && src[fe] != '\n' {
								fe++
							}
							fe++ // include newline
							var fnames []string
							for _, n := range fl.Names {
								fnames = append(fnames, n.Name)
							}
							if len(fnames) == 0 {
								continue
							}
							add(ts.Name.Name+"."+strings.Join(fnames, ","), fs, fe, func(fu *unit) {
								fu.field = true
								fu.parent = u.id
								fu.deps[u.id] = true
							})
						}
					}
				}
			}
		}
	}
}

// spanStart: first non-blank line after prevEnd.
func spanStart(src []byte, prevEnd int) int {
	i := prevEnd
	for i < len(src) && (src[i] == '\n' || src[i] == ' ' || src[i] == '\t') {
		i++
	}
	for i > 0 && src[i-1] != '\n' {
		i--
	}
	return i
}

type stage struct {
	name  string
	roots []string
	never []string
}

func readStages(p string) []stage {
	f, err := os.Open(p)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	var st []stage
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if i := strings.Index(l, "#"); i >= 0 {
			l = strings.TrimSpace(l[:i])
		}
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "stage ") {
			st = append(st, stage{name: strings.TrimPrefix(l, "stage ")})
			continue
		}
		for _, w := range strings.Fields(l) {
			if strings.HasPrefix(w, "!") {
				st[len(st)-1].never = append(st[len(st)-1].never, w[1:])
			} else {
				st[len(st)-1].roots = append(st[len(st)-1].roots, w)
			}
		}
	}
	return st
}

func match(pat string) []*unit {
	if strings.HasSuffix(pat, ":*") {
		fn := strings.TrimSuffix(pat, ":*")
		var r []*unit
		for _, u := range units {
			if u.file == fn {
				r = append(r, u)
			}
		}
		return r
	}
	if u, ok := units[pat]; ok {
		return []*unit{u}
	}
	if strings.HasSuffix(pat, ".*") {
		pre := strings.TrimSuffix(pat, "*")
		var r []*unit
		for _, u := range units {
			if strings.HasPrefix(u.name, pre) {
				r = append(r, u)
			}
		}
		return r
	}
	r := byName[pat]
	if len(r) == 0 {
		fmt.Fprintln(os.Stderr, "no unit:", pat)
	}
	return r
}

// closure computes cumulative included sets per stage.
func closure(st []stage) []map[string]bool {
	inc := map[string]bool{}
	never := map[string]bool{}
	var out []map[string]bool
	for _, s := range st {
		for _, n := range s.never {
			for _, u := range match(n) {
				never[u.id] = true
			}
		}
		var stack []string
		for _, r := range s.roots {
			for _, u := range match(r) {
				stack = append(stack, u.id)
				delete(never, u.id)
			}
		}
		for len(stack) > 0 {
			id := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if inc[id] {
				continue
			}
			inc[id] = true
			for d := range units[id].deps {
				if !inc[d] && !never[d] {
					stack = append(stack, d)
				}
			}
		}
		// pull tests whose deps are all included (non-field deps)
		for changed := true; changed; {
			changed = false
			for _, u := range units {
				if !u.test || inc[u.id] || never[u.id] {
					continue
				}
				if !strings.HasPrefix(u.name, "Test") && !strings.HasPrefix(u.name, "Fuzz") && !strings.HasPrefix(u.name, "Benchmark") && !strings.HasPrefix(u.name, "Example") {
					continue
				}
				ok := true
				var need []string
				var walk func(id string)
				seen := map[string]bool{}
				walk = func(id string) {
					if seen[id] || !ok {
						return
					}
					seen[id] = true
					if units[id].test {
						if never[id] {
							ok = false
							return
						}
						need = append(need, id)
						for d := range units[id].deps {
							walk(d)
						}
						return
					}
					if !inc[id] {
						ok = false
					}
				}
				walk(u.id)
				if ok {
					for _, n := range need {
						inc[n] = true
					}
					changed = true
				}
			}
		}
		// remove "never" units if they were added as roots? keep explicit roots
		cp := map[string]bool{}
		for k := range inc {
			cp[k] = true
		}
		out = append(out, cp)
	}
	return out
}

func lines(u *unit) int { return lineOf(srcs[u.file], u.end) - lineOf(srcs[u.file], u.start) + 1 }

func analyze(st []stage) {
	sets := closure(st)
	prev := map[string]bool{}
	for i, s := range st {
		cur := sets[i]
		perFile := map[string]int{}
		var newT []string
		total := 0
		for id := range cur {
			if prev[id] {
				continue
			}
			u := units[id]
			if u.test {
				if strings.HasPrefix(u.name, "Test") || strings.HasPrefix(u.name, "Fuzz") || strings.HasPrefix(u.name, "Bench") {
					newT = append(newT, u.name)
				}
				continue
			}
			if u.field {
				perFile[u.file] += lines(u)
				total += lines(u)
				continue
			}
			l := lines(u)
			// struct unit lines minus its fields counted separately
			for _, f := range units {
				if f.parent == u.id {
					l -= lines(f)
				}
			}
			perFile[u.file] += l + 1
			total += l + 1
		}
		var fs []string
		for f, n := range perFile {
			fs = append(fs, fmt.Sprintf("%s=%d", f, n))
		}
		sort.Strings(fs)
		sort.Strings(newT)
		fmt.Printf("== stage %d %s: ~%d non-test lines\n  %s\n  tests: %s\n", i+1, s.name, total, strings.Join(fs, " "), strings.Join(newT, " "))
		if os.Getenv("V") != "" {
			var ids []string
			for id := range cur {
				if !prev[id] && !units[id].test {
					ids = append(ids, id)
				}
			}
			sort.Strings(ids)
			if os.Getenv("V") == "2" {
				for i, id := range ids {
					ids[i] = fmt.Sprintf("%s=%d", id, lines(units[id]))
				}
			}
			fmt.Printf("  units: %s\n", strings.Join(ids, " "))
		}
		prev = cur
	}
	// remaining
	var rest []string
	for id, u := range units {
		if !prev[id] && !u.test {
			rest = append(rest, id)
		}
	}
	sort.Strings(rest)
	fmt.Printf("== not included non-test: %d\n  %s\n", len(rest), strings.Join(rest, " "))
	var restT []string
	for id, u := range units {
		if !prev[id] && u.test {
			restT = append(restT, id)
		}
	}
	sort.Strings(restT)
	fmt.Printf("== not included test: %s\n", strings.Join(restT, " "))
}

// emit writes the projected files for an included set.
func emit(inc map[string]bool, outdir string) {
	for _, base := range fileOrder {
		src := srcs[base]
		f := files[base]
		tf := fsets[base].File(f.Pos())
		var us []*unit
		for _, u := range units {
			if u.file == base && !u.field {
				us = append(us, u)
			}
		}
		sort.Slice(us, func(i, j int) bool { return us[i].start < us[j].start })
		any := false
		for _, u := range us {
			if inc[u.id] {
				any = true
			}
		}
		out := filepath.Join(outdir, base)
		if !any {
			os.Remove(out)
			continue
		}
		// header: up to end of imports (or package clause)
		hdrEnd := tf.Offset(f.Name.End())
		for _, d := range f.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.IMPORT {
				hdrEnd = tf.Offset(g.End())
			}
		}
		var b strings.Builder
		b.Write(src[:hdrEnd])
		b.WriteString("\n")
		for _, u := range us {
			if !inc[u.id] {
				continue
			}
			b.WriteString("\n")
			// drop excluded fields
			var fl []*unit
			for _, x := range units {
				if x.parent == u.id && !inc[x.id] {
					fl = append(fl, x)
				}
			}
			sort.Slice(fl, func(i, j int) bool { return fl[i].start < fl[j].start })
			if ov := override(u.id); ov != nil {
				b.Write(ov)
				b.WriteString("\n")
				continue
			}
			pos := u.start
			for _, x := range fl {
				b.Write(src[pos:x.start])
				pos = x.end
			}
			b.Write(src[pos:u.end])
			b.WriteString("\n")
		}
		res := pruneImports([]byte(b.String()), !strings.HasSuffix(base, "_test.go"))
		if err := os.WriteFile(out, res, 0o644); err != nil {
			panic(err)
		}
	}
}

// pruneImports removes unused import lines (text-based) and gofmts.
func pruneImports(src []byte, fmtIt bool) []byte {
	fset := token.NewFileSet()
	f, err := parserParse(fset, src)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse:", err)
		return src
	}
	used := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		if se, ok := n.(*ast.SelectorExpr); ok {
			if id, ok := se.X.(*ast.Ident); ok {
				used[id.Name] = true
			}
		}
		return true
	})
	tf := fset.File(f.Pos())
	type rng struct{ s, e int }
	var drop []rng
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok || g.Tok != token.IMPORT {
			continue
		}
		for _, sp := range g.Specs {
			is := sp.(*ast.ImportSpec)
			p, _ := strconv.Unquote(is.Path.Value)
			name := pkgNameGuess(p)
			if is.Name != nil {
				name = is.Name.Name
			}
			if name == "_" || name == "." || used[name] {
				continue
			}
			s := tf.Offset(is.Pos())
			for s > 0 && src[s-1] != '\n' {
				s--
			}
			e := tf.Offset(is.End())
			for e < len(src) && src[e] != '\n' {
				e++
			}
			drop = append(drop, rng{s, e + 1})
		}
	}
	var b []byte
	pos := 0
	for _, r := range drop {
		b = append(b, src[pos:r.s]...)
		pos = r.e
	}
	b = append(b, src[pos:]...)
	if !fmtIt {
		return b
	}
	out, err := format.Source(b)
	if err != nil {
		fmt.Fprintln(os.Stderr, "format:", err)
		return b
	}
	return out
}

func pkgNameGuess(p string) string {
	n := filepath.Base(p)
	if len(n) >= 2 && n[0] == 'v' && strings.Trim(n[1:], "0123456789") == "" {
		n = filepath.Base(filepath.Dir(p))
	}
	return n
}

var curStage int
var ovDir = "/tmp/ctsplit/ov"

// override returns the latest override text for id at stage <= curStage.
func override(id string) []byte {
	for k := curStage; k >= 1; k-- {
		b, err := os.ReadFile(filepath.Join(ovDir, strconv.Itoa(k), strings.ReplaceAll(id, "/", "_")))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(b)) == "FINAL" {
			return nil
		}
		return []byte(strings.TrimRight(string(b), "\n"))
	}
	return nil
}

func main() {
	switch os.Args[1] {
	case "units":
		load(os.Args[2])
		var ids []string
		for id, u := range units {
			ids = append(ids, fmt.Sprintf("%s %d deps=%d", id, lines(u), len(u.deps)))
		}
		sort.Strings(ids)
		fmt.Println(strings.Join(ids, "\n"))
	case "deps":
		load(os.Args[2])
		for _, n := range os.Args[3:] {
			for _, u := range match(n) {
				var ds []string
				for d := range u.deps {
					ds = append(ds, d)
				}
				sort.Strings(ds)
				fmt.Printf("%s -> %s\n", u.id, strings.Join(ds, " "))
			}
		}
	case "rdeps":
		load(os.Args[2])
		for _, n := range os.Args[3:] {
			for _, u := range match(n) {
				var ds []string
				for _, v := range units {
					if v.deps[u.id] {
						ds = append(ds, v.id)
					}
				}
				sort.Strings(ds)
				fmt.Printf("%s <- %s\n", u.id, strings.Join(ds, " "))
			}
		}
	case "tcost":
		load(os.Args[2])
		tcost()
	case "analyze":
		load(os.Args[2])
		analyze(readStages(os.Args[3]))
	case "emit":
		load(os.Args[2])
		st := readStages(os.Args[3])
		k, _ := strconv.Atoi(os.Args[4])
		sets := closure(st)
		curStage = k
		emit(sets[k-1], os.Args[5])
	}
}

func parserParse(fset *token.FileSet, src []byte) (*ast.File, error) {
	return parser.ParseFile(fset, "x.go", src, parser.ParseComments)
}

func tcost() {
	var out []string
	for _, u := range units {
		if !u.test || !(strings.HasPrefix(u.name, "Test") || strings.HasPrefix(u.name, "Fuzz") || strings.HasPrefix(u.name, "Bench")) {
			continue
		}
		seen := map[string]bool{}
		var st = []string{u.id}
		for len(st) > 0 {
			id := st[len(st)-1]
			st = st[:len(st)-1]
			if seen[id] {
				continue
			}
			seen[id] = true
			for d := range units[id].deps {
				st = append(st, d)
			}
		}
		n := 0
		fs := map[string]bool{}
		for id := range seen {
			v := units[id]
			if v.test {
				continue
			}
			fs[strings.TrimSuffix(v.file, ".go")] = true
			if v.field {
				n += lines(v)
			} else {
				n += lines(v) + 1
			}
		}
		var fl []string
		for f := range fs {
			fl = append(fl, f)
		}
		sort.Strings(fl)
		out = append(out, fmt.Sprintf("%5d %s [%s]", n, u.id, strings.Join(fl, " ")))
	}
	sort.Strings(out)
	fmt.Println(strings.Join(out, "\n"))
}
