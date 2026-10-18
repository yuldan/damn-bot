package archtest

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// modulePath = path modul go.mod utama.
const modulePath = "github.com/damnbot/damnbot"

// repoDir = root repo relatif direktori paket ini (sama dengan readRepoFile).
func repoDir() string { return filepath.Join("..", "..") }

// goFile = satu file Go modul utama: path relatif root (slash), isi, AST (dengan komentar)
// dan FileSet-nya.
type goFile struct {
	path string
	src  []byte
	ast  *ast.File
	fset *token.FileSet
}

func (f goFile) isTest() bool { return strings.HasSuffix(f.path, "_test.go") }

func (f goFile) line(p token.Pos) int { return f.fset.Position(p).Line }

// lines = jumlah baris fisik (`wc -l`, ditambah baris terakhir tanpa newline).
func (f goFile) lines() int {
	n := bytes.Count(f.src, []byte("\n"))
	if len(f.src) > 0 && f.src[len(f.src)-1] != '\n' {
		n++
	}
	return n
}

// openRepo membuka root repo lewat os.Root (akses terbatas di dalam repo).
func openRepo(t *testing.T) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(repoDir())
	if err != nil {
		t.Fatalf("open repo root: %v", err)
	}
	t.Cleanup(func() {
		if cerr := root.Close(); cerr != nil {
			t.Errorf("close repo root: %v", cerr)
		}
	})
	return root
}

// skipDir: direktori yang bukan bagian modul utama (modul lain = punya go.mod sendiri, mis.
// bench/ dan tools/) atau diabaikan toolchain Go (., _, testdata, vendor).
func skipDir(fsys fs.FS, p, name string) bool {
	if p == "." {
		return false
	}
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" || name == "vendor" {
		return true
	}
	_, err := fs.Stat(fsys, path.Join(p, "go.mod"))
	return err == nil
}

// mainModuleGoFiles mem-parse semua file .go modul utama (semua GOOS/tag; tanpa filter build).
func mainModuleGoFiles(t *testing.T) []goFile {
	t.Helper()
	fsys := openRepo(t).FS()
	fset := token.NewFileSet()
	var out []goFile
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir() && skipDir(fsys, p, d.Name()):
			return fs.SkipDir
		case d.IsDir() || !strings.HasSuffix(p, ".go"):
			return nil
		}
		src, rerr := fs.ReadFile(fsys, p)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", p, rerr)
		}
		f, perr := parser.ParseFile(fset, p, src, parser.ParseComments|parser.SkipObjectResolution)
		if perr != nil {
			t.Errorf("parse %s: %v", p, perr)
			return nil
		}
		out = append(out, goFile{path: p, src: src, ast: f, fset: fset})
		return nil
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("tidak ada file Go di modul utama")
	}
	return out
}

// runRepo menjalankan cmd (argumen literal di pemanggil) dari root repo dengan env tambahan dan
// mengembalikan stdout.
func runRepo(t *testing.T, cmd *exec.Cmd, env ...string) []byte {
	t.Helper()
	cmd.Dir = repoDir()
	cmd.Env = append(os.Environ(), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s: %v\n%s", strings.Join(cmd.Args, " "), err, stderr.String())
	}
	return out
}

// nolintLinters mengembalikan daftar linter dan alasan dari komentar `//nolint:a,b // alasan`;
// ok=false bila komentar bukan direktif nolint.
func nolintLinters(text string) (linters []string, reason string, ok bool) {
	rest, ok := strings.CutPrefix(text, "//nolint")
	if !ok || (rest != "" && rest[0] != ':' && rest[0] != ' ') {
		return nil, "", false
	}
	directive, reason, _ := strings.Cut(rest, "//")
	if names, named := strings.CutPrefix(strings.TrimSpace(directive), ":"); named && names != "" {
		linters = strings.Split(names, ",")
	}
	return linters, strings.TrimSpace(reason), true
}
