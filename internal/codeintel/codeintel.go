// Package codeintel indexes a workspace without executing project code
// (CODE-TOOLS §2/B.4, ADR-015): Go symbols through go/ast, a deterministic
// repository map, and later tree-sitter/LSP behind the same trust gate.
package codeintel

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/codeintel/ts"
)

// Limits of one index pass.
const (
	MaxFiles     = 10000
	MaxFileBytes = 1 << 20
	MaxSymbols   = 100000
)

// Errors.
var (
	ErrRoot     = errors.New("codeintel: invalid root")
	ErrTooLarge = errors.New("codeintel: workspace too large")
	// ErrUnsupportedLanguage: a requested grammar is outside {c,cpp}.
	ErrUnsupportedLanguage = errors.New("codeintel: unsupported language")
)

// skipEntry keeps one countable skip from aborting the walk.
func skipEntry() error { return nil }

// SymbolKind is the closed symbol set of the repository map.
type SymbolKind string

// Symbol kinds.
const (
	KindPackage SymbolKind = "package"
	KindType    SymbolKind = "type"
	KindFunc    SymbolKind = "func"
	KindMethod  SymbolKind = "method"
	KindImport  SymbolKind = "import"
)

// Symbol is one definition in a file.
type Symbol struct {
	Kind     SymbolKind `json:"kind"`
	Name     string     `json:"name"`
	Receiver string     `json:"receiver,omitempty"`
	Line     int        `json:"line"`
	Col      int        `json:"col"`
}

// File is one indexed Go file.
type File struct {
	Path    string   `json:"path"` // slash-separated, relative to Root
	Package string   `json:"package"`
	Symbols []Symbol `json:"symbols"`
	Imports []string `json:"imports,omitempty"`
	score   float64
}

// Skipped counts files excluded from the index; the footer never hides them.
type Skipped struct {
	Binary     int `json:"binary"`
	Large      int `json:"large"`
	Unreadable int `json:"unreadable"`
	Symlink    int `json:"symlink"`
	NonGo      int `json:"non_go"`
}

// Map is the deterministic repository index.
type Map struct {
	Root    string  `json:"root"`
	Files   []File  `json:"files"`
	Skipped Skipped `json:"skipped"`
}

// Options bounds one index pass. Root is required; zero limits use defaults.
type Options struct {
	Root         string
	MaxFiles     int
	MaxFileBytes int64
	Focus        string
	// Languages selects non-Go grammars ({"c","cpp"}); empty means Go
	// only. Other languages are ErrUnsupportedLanguage when requested.
	Languages []string
}

// Index walks opts.Root and parses Go files without executing anything.
// Symlinks are never followed: a link inside the root that points outside is
// counted in Skipped.Symlink and cannot inject outside content into the map.
//
//nolint:gocognit,gocyclo,funlen // one walker state machine; splitting hides skip accounting
func Index(ctx context.Context, opts Options) (*Map, error) {
	if opts.Root == "" {
		return nil, fmt.Errorf("%w: empty root", ErrRoot)
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRoot, err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrRoot, err)
	}
	if st, serr := os.Stat(root); serr != nil || !st.IsDir() {
		return nil, fmt.Errorf("%w: %s is not a directory", ErrRoot, root)
	}
	for _, lang := range opts.Languages {
		if !ts.Supported(ts.Language(lang)) {
			return nil, fmt.Errorf("%w: %q", ErrUnsupportedLanguage, lang)
		}
	}
	maxFiles := opts.MaxFiles
	if maxFiles <= 0 {
		maxFiles = MaxFiles
	}
	maxBytes := opts.MaxFileBytes
	if maxBytes <= 0 {
		maxBytes = MaxFileBytes
	}
	m := &Map{Root: root}
	var count, symbols int
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			m.Skipped.Unreadable++
			return skipEntry()
		}
		if cerr := ctx.Err(); cerr != nil {
			return fmt.Errorf("codeintel: index: %w", cerr)
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".hg", ".svn", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			// Never follow: even a link that stays inside the root is
			// excluded so the map cannot be steered through a link swap.
			m.Skipped.Symlink++
			return nil
		}
		lang, isGo := languageFor(d.Name(), opts.Languages)
		if !isGo && lang == "" {
			m.Skipped.NonGo++
			return nil
		}
		if count >= maxFiles {
			return ErrTooLarge
		}
		info, ierr := d.Info()
		if ierr != nil {
			m.Skipped.Unreadable++
			return skipEntry()
		}
		if info.Size() > maxBytes {
			m.Skipped.Large++
			return nil
		}
		//nolint:gosec // path comes from WalkDir under the canonical root
		src, rerr := os.ReadFile(path)
		if rerr != nil {
			m.Skipped.Unreadable++
			return skipEntry()
		}
		if binary(src) {
			m.Skipped.Binary++
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			m.Skipped.Unreadable++
			return skipEntry()
		}
		var f File
		var perr error
		if isGo {
			f, perr = parseGo(filepath.ToSlash(rel), src)
		} else {
			var syms []ts.Symbol
			syms, perr = ts.Parse(ctx, ts.Language(lang), src, 0, 0)
			f = File{Path: filepath.ToSlash(rel)}
			for _, sym := range syms {
				f.Symbols = append(f.Symbols, Symbol{Kind: SymbolKind(sym.Kind), Name: sym.Name, Line: sym.Line, Col: sym.Col})
			}
		}
		if perr != nil {
			// A file that is not valid Go is reported, not fatal: the map is
			// a navigation aid and one broken file must not hide the repo.
			m.Skipped.Unreadable++
			return skipEntry()
		}
		symbols += len(f.Symbols)
		if symbols > MaxSymbols {
			return ErrTooLarge
		}
		m.Files = append(m.Files, f)
		count++
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("codeintel: walk: %w", err)
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	rank(m, opts.Focus)
	return m, nil
}

// binary applies the CODE-TOOLS sniff: NUL in the first 8 KiB or more than
// 10% invalid UTF-8 means the file is not source.
func binary(src []byte) bool {
	head := src
	if len(head) > 8<<10 {
		head = head[:8<<10]
	}
	if strings.IndexByte(string(head), 0) >= 0 {
		return true
	}
	invalid := 0
	for i := 0; i < len(head); {
		r, size := utf8.DecodeRune(head[i:])
		if r == utf8.RuneError && size == 1 {
			invalid++
		}
		i += size
	}
	return invalid*10 > len(head)
}

// languageFor maps a file name to the selected non-Go grammar.
func languageFor(name string, langs []string) (lang string, isGo bool) {
	if strings.HasSuffix(name, ".go") {
		return "", true
	}
	ext := strings.ToLower(filepath.Ext(name))
	var want string
	switch ext {
	case ".c", ".h":
		want = string(ts.C)
	case ".cc", ".cpp", ".cxx", ".hpp", ".hh", ".hxx":
		want = string(ts.Cpp)
	default:
		return "", false
	}
	if slices.Contains(langs, want) {
		return want, false
	}
	return "", false
}
