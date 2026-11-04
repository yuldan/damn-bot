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
	"io/fs"
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
var cache = wazero.NewCompilationCache() //nolint:gochecknoglobals // E-10 allowlist: immutable wazero compilation cache shared read-only.

// Limits.
const (
	DefaultMemoryPages = 1024 // 64 MiB
	DefaultTimeoutMS   = 60000
	MaxNodes           = 20000
	MaxSourceBytes     = 1 << 20
)

// Language is the closed set of non-Go grammars in this build.
type Language string

// Supported languages (the embedded module ships these grammars).
const (
	C      Language = "c"
	Cpp    Language = "cpp"
	JS     Language = "javascript"
	TS     Language = "typescript"
	TSX    Language = "tsx"
	Python Language = "python"
)

// assetName is the single self-contained tree-sitter runtime + grammar module.
const assetName = "ts.wasm"

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
// are test seams; zero uses the defaults (64 MiB, 60 s).
func Parse(ctx context.Context, lang Language, source []byte, memoryPages, timeoutMS uint32) ([]Symbol, error) {
	return parseFS(ctx, lang, source, memoryPages, timeoutMS, assets)
}

// parseFS is the fail-closed seam: a missing asset is ErrParse for that file,
// never a panic or silent empty result.
func parseFS(ctx context.Context, lang Language, source []byte, memoryPages, timeoutMS uint32, fsys fs.FS) ([]Symbol, error) {
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
	wasm, err := fs.ReadFile(fsys, assetName)
	if err != nil {
		return nil, fmt.Errorf("%w: grammar asset %s: %w", ErrParse, lang, err)
	}
	compiled, err := r.CompileModule(ctx, wasm)
	if err != nil {
		return nil, fmt.Errorf("%w: compile: %w", ErrParse, err)
	}
	mod, err := r.InstantiateModule(ctx, compiled, wazero.NewModuleConfig())
	if err != nil {
		return nil, fmt.Errorf("%w: instantiate: %w", ErrParse, err)
	}
	p := &parser{mod: mod, src: source, lang: lang}
	return p.parse(ctx, lang)
}

// Supported reports whether lang is in the closed grammar set.
func Supported(lang Language) bool {
	switch lang {
	case C, Cpp, JS, TS, TSX, Python:
		return true
	}
	return false
}

// grammarExport maps a language to its wasm export.
func grammarExport(lang Language) (string, bool) {
	switch lang {
	case C:
		return "tree_sitter_c", true
	case Cpp:
		return "tree_sitter_cpp", true
	case JS:
		return "tree_sitter_javascript", true
	case TS:
		return "tree_sitter_typescript", true
	case TSX:
		return "tree_sitter_tsx", true
	case Python:
		return "tree_sitter_python", true
	}
	return "", false
}

// parser holds the wasm handles of one bounded parse.
type parser struct {
	mod     api.Module
	src     []byte
	lang    Language
	visited int
}

//nolint:gocyclo // one wasm call sequence; splitting hides the handle order
func (p *parser) parse(ctx context.Context, lang Language) ([]Symbol, error) {
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
	langFnName, ok := grammarExport(lang)
	if !ok {
		return nil, fmt.Errorf("%w: missing grammar for %s", ErrParse, lang)
	}
	langFn := p.mod.ExportedFunction(langFnName)
	if langFn == nil {
		return nil, fmt.Errorf("%w: missing %s", ErrParse, langFnName)
	}
	srcPtr, err := malloc.Call(ctx, uint64(len(p.src))+1)
	if err != nil {
		return nil, fmt.Errorf("%w: alloc source: %w", ErrParse, err)
	}
	if !p.mod.Memory().Write(wasm32(srcPtr[0]), p.src) {
		return nil, fmt.Errorf("%w: write source", ErrParse)
	}
	pv, err := pnew.Call(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: parser: %w", ErrParse, err)
	}
	defer func() { discard(pdel.Call(ctx, pv[0])) }()
	lv, err := langFn.Call(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: language: %w", ErrParse, err)
	}
	setv, err := pset.Call(ctx, pv[0], lv[0])
	if err != nil || setv[0] == 0 {
		return nil, fmt.Errorf("%w: set language", ErrParse)
	}
	tv, err := pparse.Call(ctx, pv[0], 0, srcPtr[0], uint64(len(p.src)))
	if err != nil {
		return nil, fmt.Errorf("%w: parse: %w", ErrParse, err)
	}
	nodePtr, err := malloc.Call(ctx, 24)
	if err != nil {
		return nil, fmt.Errorf("%w: node alloc: %w", ErrParse, err)
	}
	if _, err := root.Call(ctx, nodePtr[0], tv[0]); err != nil {
		return nil, fmt.Errorf("%w: root: %w", ErrParse, err)
	}
	if _, err := free.Call(ctx, srcPtr[0]); err != nil {
		return nil, fmt.Errorf("%w: free: %w", ErrParse, err)
	}
	syms := &symbols{src: p.src}
	if err := p.walk(ctx, nodePtr[0], ntype, startF, endF, nchild, childF, malloc, syms, false); err != nil {
		return nil, err
	}
	return syms.out, nil
}

// walk visits named nodes up to MaxNodes/depth, collecting definitions.
// inClass tracks Python class bodies so a nested def is a method, while a def
// nested inside that method is a plain function again.
func (p *parser) walk(ctx context.Context, node uint64, ntype, start, end, nchild, child api.Function, malloc api.Function, out *symbols, inClass bool) error {
	if p.visited > MaxNodes || ctx.Err() != nil {
		return fmt.Errorf("%w: node budget", ErrParse)
	}
	p.visited++
	kind, err := p.nodeString(ctx, ntype, node)
	if err != nil {
		return err
	}
	dk := p.defKind(kind, inClass)
	if dk == "const" && !p.hasPrefix(ctx, start, end, node, "const") {
		dk = ""
	}
	if dk != "" {
		if sb, nerr := p.findName(ctx, node, ntype, start, end, nchild, child, malloc); nerr == nil && sb.line > 0 {
			sb.kind = dk
			out.add(sb)
		}
	}
	nextClass := classContext(kind, inClass)
	count, err := nchild.Call(ctx, node)
	if err != nil {
		return fmt.Errorf("%w: child count: %w", ErrParse, err)
	}
	for i := range count[0] {
		buf, err := malloc.Call(ctx, 24)
		if err != nil {
			return fmt.Errorf("%w: child alloc: %w", ErrParse, err)
		}
		if _, err := child.Call(ctx, buf[0], node, i); err != nil {
			return fmt.Errorf("%w: child: %w", ErrParse, err)
		}
		if err := p.walk(ctx, buf[0], ntype, start, end, nchild, child, malloc, out, nextClass); err != nil {
			return err
		}
	}
	return nil
}

// classContext reports whether descendants of kind are still inside a class
// body. A function resets the context so nested defs are not methods.
func classContext(kind string, inClass bool) bool {
	switch kind {
	case "class_definition", "class_declaration":
		return true
	case "function_definition", "function_declaration", "method_definition", "arrow_function":
		return false
	}
	return inClass
}

// hasPrefix reports whether the node text starts with prefix.
func (p *parser) hasPrefix(ctx context.Context, start, end api.Function, node uint64, prefix string) bool {
	s, e, err := p.span(ctx, start, end, node)
	if err != nil || e > uint64(len(p.src)) || e-s < uint64(len(prefix)) {
		return false
	}
	return string(p.src[s:s+uint64(len(prefix))]) == prefix
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

func (p *parser) findName(ctx context.Context, node uint64, ntype, start, end, nchild, child, malloc api.Function) (symbol, error) {
	kind, err := p.nodeString(ctx, ntype, node)
	if err != nil {
		return symbol{}, err
	}
	if kind == "identifier" || kind == "field_identifier" || kind == "property_identifier" || kind == "type_identifier" {
		s, e, serr := p.span(ctx, start, end, node)
		if serr != nil || s >= e || e > uint64(len(p.src)) {
			return symbol{}, serr
		}
		line, col := lineCol(p.src, s)
		return symbol{name: string(p.src[s:e]), line: line, col: col}, nil
	}
	count, err := nchild.Call(ctx, node)
	if err != nil {
		return symbol{}, fmt.Errorf("%w: child count: %w", ErrParse, err)
	}
	for i := range count[0] {
		buf, merr := malloc.Call(ctx, 24)
		if merr != nil {
			return symbol{}, fmt.Errorf("%w: child alloc: %w", ErrParse, merr)
		}
		if _, cerr := child.Call(ctx, buf[0], node, i); cerr != nil {
			return symbol{}, fmt.Errorf("%w: child: %w", ErrParse, cerr)
		}
		if sym, err := p.findName(ctx, buf[0], ntype, start, end, nchild, child, malloc); err == nil && sym.name != "" {
			return sym, nil
		}
	}
	return symbol{}, nil
}

func (p *parser) span(ctx context.Context, start, end api.Function, node uint64) (uint64, uint64, error) {
	s, err := start.Call(ctx, node)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: start: %w", ErrParse, err)
	}
	e, err := end.Call(ctx, node)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: end: %w", ErrParse, err)
	}
	return s[0], e[0], nil
}

func (p *parser) nodeString(ctx context.Context, fn api.Function, node uint64) (string, error) {
	ptr, err := fn.Call(ctx, node)
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
func (p *parser) defKind(nodeType string, inClass bool) string {
	switch nodeType {
	case "function_definition":
		if p.lang == Python && inClass {
			return "method"
		}
		return "func"
	case "function_declaration":
		return "func"
	case "method_definition", "method_signature":
		return "method"
	case "struct_specifier":
		return "struct"
	case "union_specifier":
		return "union"
	case "enum_specifier", "enum_declaration":
		return "enum"
	case "class_specifier", "class_declaration", "class_definition":
		return "class"
	case "namespace_definition", "internal_module":
		return "namespace"
	case "type_definition", "interface_declaration", "type_alias_declaration":
		return "type"
	case "lexical_declaration":
		return "const"
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
func closeRuntime(ctx context.Context, r wazero.Runtime) { discard(nil, r.Close(ctx)) }

// discard consumes teardown results that are best-effort.
func discard(_ []uint64, _ error) {}

// wasm32 narrows a wasm pointer (guaranteed 32-bit by the module ABI).
func wasm32(v uint64) uint32 {
	//nolint:gosec // the wasm ABI defines pointers as 32-bit
	return uint32(v)
}
