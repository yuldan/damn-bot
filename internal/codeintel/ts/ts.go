// Package ts parses non-Go source with a tree-sitter grammar compiled to
// WebAssembly and executed by wazero (no CGO, ADR-015/089). Each parse gets
// its own WebAssembly runtime with a hard memory cap and the caller's
// deadline; a failed or timed-out parse is an error for that file only.
package ts

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed ts.wasm
var assets embed.FS

// cache is the shared, immutable wazero compilation cache: compiling the
// 4,2 MB module once makes parallel parses bounded by parsing, not compilation.
//
//nolint:gochecknoglobals // immutable cache shared by every Parse call
var cache = wazero.NewCompilationCache()

// Limits.
const (
	DefaultMemoryPages = 1024 // 64 MiB
	DefaultTimeoutMS   = 30000
	MaxNodes           = 20000
	MaxSourceBytes     = 1 << 20
)

// Language is the closed set of non-Go grammars in this build.
type Language string

// Supported languages (the embedded module ships the C and C++ grammars).
const (
	C   Language = "c"
	Cpp Language = "cpp"
)

// ErrUnsupportedLanguage is returned for any grammar not in the closed set.
var ErrUnsupportedLanguage = errors.New("codeintel/ts: unsupported language")

// ErrParse marks a file the parser could not handle (corrupt, too nested, or
// over the memory/time budget); callers count it as skipped, never fatal.
var ErrParse = errors.New("codeintel/ts: parse failed")

// Symbol is one definition found in a non-Go file.
type Symbol struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
}

// Parse returns the definitions of source in lang. memoryPages and timeoutMS
// are test seams; zero uses the defaults (64 MiB, 2 s).
func Parse(ctx context.Context, lang Language, source []byte, memoryPages, timeoutMS uint32) ([]Symbol, error) {
	if !Supported(lang) {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedLanguage, lang)
	}
	if len(source) > MaxSourceBytes {
		return nil, fmt.Errorf("%w: source too large", ErrParse)
	}
	if memoryPages == 0 {
		memoryPages = DefaultMemoryPages
	}
	if timeoutMS == 0 {
		timeoutMS = DefaultTimeoutMS
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()
	r := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithMemoryLimitPages(memoryPages).
		WithCloseOnContextDone(true).
		WithCompilationCache(cache))
	defer closeRuntime(context.WithoutCancel(ctx), r)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r); err != nil {
		return nil, fmt.Errorf("%w: wasi: %w", ErrParse, err)
	}
	wasm, err := assets.ReadFile("ts.wasm")
	if err != nil {
		return nil, fmt.Errorf("%w: asset: %w", ErrParse, err)
	}
	compiled, err := r.CompileModule(ctx, wasm)
	if err != nil {
		return nil, fmt.Errorf("%w: compile: %w", ErrParse, err)
	}
	mod, err := r.InstantiateModule(ctx, compiled, wazero.NewModuleConfig())
	if err != nil {
		return nil, fmt.Errorf("%w: instantiate: %w", ErrParse, err)
	}
	p := &parser{ctx: ctx, mod: mod, src: source}
	return p.parse(lang) //nolint:contextcheck // p.ctx is the same bounded context
}

// Supported reports whether lang is in the closed grammar set.
func Supported(lang Language) bool {
	switch lang {
	case C, Cpp:
		return true
	}
	return false
}

// parser holds the wasm handles of one bounded parse.
//
//nolint:containedctx // one parser value lives for exactly one bounded parse
type parser struct {
	ctx     context.Context
	mod     api.Module
	src     []byte
	visited int
}

//nolint:gocyclo // one wasm call sequence; splitting hides the handle order
func (p *parser) parse(lang Language) ([]Symbol, error) {
	malloc := p.mod.ExportedFunction("malloc")
	free := p.mod.ExportedFunction("free")
	pnew := p.mod.ExportedFunction("ts_parser_new")
	pset := p.mod.ExportedFunction("ts_parser_set_language")
	pparse := p.mod.ExportedFunction("ts_parser_parse_string")
	pdel := p.mod.ExportedFunction("ts_parser_delete")
	root := p.mod.ExportedFunction("ts_tree_root_node")
	ntype := p.mod.ExportedFunction("ts_node_type")
	startF := p.mod.ExportedFunction("ts_node_start_byte")
	endF := p.mod.ExportedFunction("ts_node_end_byte")
	nchild := p.mod.ExportedFunction("ts_node_named_child_count")
	childF := p.mod.ExportedFunction("ts_node_named_child")
	for _, fn := range []api.Function{malloc, free, pnew, pset, pparse, pdel, root, ntype, startF, endF, nchild, childF} {
		if fn == nil {
			return nil, fmt.Errorf("%w: missing export", ErrParse)
		}
	}
	langFnName := "tree_sitter_c"
	if lang == Cpp {
		langFnName = "tree_sitter_cpp"
	}
	langFn := p.mod.ExportedFunction(langFnName)
	if langFn == nil {
		return nil, fmt.Errorf("%w: missing %s", ErrParse, langFnName)
	}
	srcPtr, err := malloc.Call(p.ctx, uint64(len(p.src))+1)
	if err != nil {
		return nil, fmt.Errorf("%w: alloc source: %w", ErrParse, err)
	}
	if !p.mod.Memory().Write(wasm32(srcPtr[0]), p.src) {
		return nil, fmt.Errorf("%w: write source", ErrParse)
	}
	pv, err := pnew.Call(p.ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: parser: %w", ErrParse, err)
	}
	//nolint:errcheck // parser delete is best effort at scope exit
	defer func() { _, _ = pdel.Call(context.Background(), pv[0]) }()
	lv, err := langFn.Call(p.ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: language: %w", ErrParse, err)
	}
	setv, err := pset.Call(p.ctx, pv[0], lv[0])
	if err != nil || setv[0] == 0 {
		return nil, fmt.Errorf("%w: set language", ErrParse)
	}
	tv, err := pparse.Call(p.ctx, pv[0], 0, srcPtr[0], uint64(len(p.src)))
	if err != nil {
		return nil, fmt.Errorf("%w: parse: %w", ErrParse, err)
	}
	nodePtr, err := malloc.Call(p.ctx, 24)
	if err != nil {
		return nil, fmt.Errorf("%w: node alloc: %w", ErrParse, err)
	}
	if _, err := root.Call(p.ctx, nodePtr[0], tv[0]); err != nil {
		return nil, fmt.Errorf("%w: root: %w", ErrParse, err)
	}
	if _, err := free.Call(p.ctx, srcPtr[0]); err != nil {
		return nil, fmt.Errorf("%w: free: %w", ErrParse, err)
	}
	syms := &symbols{src: p.src}
	if err := p.walk(nodePtr[0], ntype, startF, endF, nchild, childF, malloc, syms); err != nil {
		return nil, err
	}
	return syms.out, nil
}

// walk visits named nodes up to MaxNodes/depth, collecting definitions.
func (p *parser) walk(node uint64, ntype, start, end, nchild, child api.Function, malloc api.Function, out *symbols) error {
	if p.visited > MaxNodes || p.ctx.Err() != nil {
		return fmt.Errorf("%w: node budget", ErrParse)
	}
	p.visited++
	kind, err := p.nodeString(ntype, node)
	if err != nil {
		return err
	}
	if defKind(kind) != "" {
		if sb, nerr := p.findName(node, ntype, start, end, nchild, child, malloc); nerr == nil && sb.line > 0 {
			sb.kind = defKind(kind)
			out.add(sb)
		}
	}
	count, err := nchild.Call(p.ctx, node)
	if err != nil {
		return fmt.Errorf("%w: child count: %w", ErrParse, err)
	}
	for i := range count[0] {
		buf, err := malloc.Call(p.ctx, 24)
		if err != nil {
			return fmt.Errorf("%w: child alloc: %w", ErrParse, err)
		}
		if _, err := child.Call(p.ctx, buf[0], node, i); err != nil {
			return fmt.Errorf("%w: child: %w", ErrParse, err)
		}
		if err := p.walk(buf[0], ntype, start, end, nchild, child, malloc, out); err != nil {
			return err
		}
	}
	return nil
}

// findName returns the first identifier below node (the declarator name).
type symbol struct {
	kind string
	name string
	line int
	col  int
}

type symbols struct {
	src []byte
	out []Symbol
}

func (s *symbols) add(sym symbol) {
	if len(s.out) >= 1000 || sym.name == "" {
		return
	}
	s.out = append(s.out, Symbol{Kind: sym.kind, Name: sym.name, Line: sym.line, Col: sym.col})
}

func (p *parser) findName(node uint64, ntype, start, end, nchild, child, malloc api.Function) (symbol, error) {
	kind, err := p.nodeString(ntype, node)
	if err != nil {
		return symbol{}, err
	}
	if kind == "identifier" || kind == "field_identifier" || kind == "type_identifier" {
		s, e, serr := p.span(start, end, node)
		if serr != nil || s >= e || e > uint64(len(p.src)) {
			return symbol{}, serr
		}
		line, col := lineCol(p.src, s)
		return symbol{name: string(p.src[s:e]), line: line, col: col}, nil
	}
	count, err := nchild.Call(p.ctx, node)
	if err != nil {
		return symbol{}, fmt.Errorf("%w: child count: %w", ErrParse, err)
	}
	for i := range count[0] {
		buf, merr := malloc.Call(p.ctx, 24)
		if merr != nil {
			return symbol{}, fmt.Errorf("%w: child alloc: %w", ErrParse, merr)
		}
		if _, cerr := child.Call(p.ctx, buf[0], node, i); cerr != nil {
			return symbol{}, fmt.Errorf("%w: child: %w", ErrParse, cerr)
		}
		if sym, err := p.findName(buf[0], ntype, start, end, nchild, child, malloc); err == nil && sym.name != "" {
			return sym, nil
		}
	}
	return symbol{}, nil
}

func (p *parser) span(start, end api.Function, node uint64) (uint64, uint64, error) {
	s, err := start.Call(p.ctx, node)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: start: %w", ErrParse, err)
	}
	e, err := end.Call(p.ctx, node)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: end: %w", ErrParse, err)
	}
	return s[0], e[0], nil
}

func (p *parser) nodeString(fn api.Function, node uint64) (string, error) {
	ptr, err := fn.Call(p.ctx, node)
	if err != nil {
		return "", fmt.Errorf("%w: node type: %w", ErrParse, err)
	}
	mem := p.mod.Memory()
	buf, ok := mem.Read(wasm32(ptr[0]), 128)
	if !ok {
		return "", fmt.Errorf("%w: node type read", ErrParse)
	}
	if i := strings.IndexByte(string(buf), 0); i >= 0 {
		return string(buf[:i]), nil
	}
	return string(buf), nil
}

// defKind maps a grammar node type to our closed symbol kind ("" = ignore).
func defKind(nodeType string) string {
	switch nodeType {
	case "function_definition":
		return "func"
	case "struct_specifier":
		return "struct"
	case "union_specifier":
		return "union"
	case "enum_specifier":
		return "enum"
	case "class_specifier":
		return "class"
	case "namespace_definition":
		return "namespace"
	case "type_definition":
		return "type"
	}
	return ""
}

func lineCol(src []byte, off uint64) (line, col int) {
	line, col = 1, 1
	for i := uint64(0); i < off && i < uint64(len(src)); i++ {
		if src[i] == '\n' {
			line++
			col = 1
			continue
		}
		col++
	}
	return line, col
}

// closeRuntime releases the per-parse wasm runtime.
func closeRuntime(ctx context.Context, r wazero.Runtime) {
	//nolint:errcheck // best effort after the result is known
	_ = r.Close(ctx)
}

// wasm32 narrows a wasm pointer (guaranteed 32-bit by the module ABI).
func wasm32(v uint64) uint32 {
	//nolint:gosec // the wasm ABI defines pointers as 32-bit
	return uint32(v)
}
