// Package gencorpus membangkitkan korpus sintetis code tools C2/C3 (CODE-TOOLS §10.1) secara
// deterministik dari seed: ≈ files/16 direktori, 12 bahasa, `.gitignore` berlapis, 1% biner,
// 0,1% file > 1 MB, file minified satu baris (50 per 80.000 file, minimal 1).
package gencorpus

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Lang = satu bahasa: ekstensi + templat baris (%s = identifier, %d = angka).
type lang struct {
	ext   string
	lines []string
}

var langs = []lang{
	{".go", []string{"func %s(ctx context.Context, n int) error {", "\tif err := %s(ctx); err != nil {", "\t\treturn fmt.Errorf(\"%s: %%w\", err)", "\t}", "\tx := %d", "}"}},
	{".py", []string{"def %s(self, n=%d):", "    return self.%s(n)", "class %s:", "    pass", "# TODO %s"}},
	{".js", []string{"function %s(a, b) {", "  return a.%s(b + %d);", "}", "const %s = require('x');"}},
	{".ts", []string{"export function %s(a: number): number {", "  return %s(a) + %d;", "}", "interface %s { id: string }"}},
	{".rs", []string{"fn %s(x: u32) -> u32 {", "    %s(x + %d)", "}", "struct %s { v: Vec<u8> }"}},
	{".java", []string{"public class %s {", "  int %s(int n) { return n + %d; }", "}"}},
	{".c", []string{"static int %s(int n) {", "  return %s(n - %d);", "}", "#define %s 1"}},
	{".cpp", []string{"int %s::run(int n) {", "  return helper_%s(n * %d);", "}"}},
	{".rb", []string{"def %s(n)", "  %s(n - %d)", "end"}},
	{".sh", []string{"%s() {", "  echo \"%s\" %d", "}"}},
	{".md", []string{"# %s", "Paragraph about %s and %d things.", ""}},
	{".yaml", []string{"%s:", "  value: %d", "  name: %s"}},
}

var vocab = []string{
	"parse", "render", "handler", "config", "session", "token", "buffer", "stream", "request", "response",
	"client", "server", "cache", "index", "query", "search", "match", "pattern", "worker", "queue",
	"lease", "fence", "journal", "commit", "rollback", "snapshot", "budget", "ledger", "schema", "validate",
}

// Params = parameter generator.
type Params struct {
	Seed  uint64
	Files int
	// MeanBytes = rata-rata ukuran file teks (C2 ≈ 13.750 B → ≈ 1,1 GB; C3 ≈ 12.000 B → ≈ 12 MB).
	MeanBytes int
}

// Stats = ringkasan korpus yang dibuat.
type Stats struct {
	Files, Dirs, Binary, Large, Minified, Ignored int
	Bytes                                         int64
	// Digest = SHA-256 atas manifest "sha256  path\n" berurut path (isi bench/corpora.lock).
	Digest string
}

// Generate menulis korpus ke root (harus belum ada).
func Generate(root string, p Params) (Stats, error) {
	if _, err := os.Stat(root); err == nil {
		return Stats{}, fmt.Errorf("gencorpus: %s sudah ada", root)
	}
	r := rand.New(rand.NewPCG(p.Seed, p.Seed^0x9e3779b97f4a7c15))
	// ≈ files/27 direktori daun; dengan direktori perantara total ≈ 5.000 untuk 80.000 file.
	ndirs := max(1, p.Files/27)
	dirs := make([]string, ndirs)
	for i := range dirs {
		depth := 1 + r.IntN(4)
		parts := make([]string, depth)
		for j := range parts {
			parts[j] = fmt.Sprintf("%s_%d", vocab[r.IntN(len(vocab))], r.IntN(8))
		}
		dirs[i] = filepath.Join(parts...)
	}
	// minified: indeks i ≡ 21 (mod 1600) → 50 per 80.000 file; tidak pernah bertabrakan dengan
	// biner (i ≡ 7 mod 100) atau file besar (i ≡ 13 mod 1000).
	const minifiedEvery = 80000 / 50
	var st Stats
	for i := range p.Files {
		dir := dirs[r.IntN(len(dirs))]
		l := langs[r.IntN(len(langs))]
		name := fmt.Sprintf("%s_%d%s", vocab[r.IntN(len(vocab))], i, l.ext)
		var data []byte
		switch {
		case i%100 == 7: // 1% biner (NUL + byte acak)
			name = fmt.Sprintf("blob_%d.bin", i)
			data = make([]byte, 512+r.IntN(4096))
			for k := range data {
				data[k] = byte(r.Uint32())
			}
			data[0] = 0
			st.Binary++
		case i%1000 == 13: // 0,1% file > 1 MB
			data = textFile(r, l, 1<<20+r.IntN(1<<19))
			st.Large++
		case i%minifiedEvery == 21: // minified: satu baris panjang
			name = fmt.Sprintf("bundle_%d.min.js", i)
			data = []byte(strings.ReplaceAll(string(textFile(r, langs[2], 200_000)), "\n", ";"))
			st.Minified++
		default:
			data = textFile(r, l, p.MeanBytes/2+r.IntN(p.MeanBytes))
		}
		if err := write(filepath.Join(root, dir, name), data); err != nil {
			return Stats{}, err
		}
	}
	// .gitignore berlapis: root + ≈ 1/8 direktori; tiap level mengabaikan pola berbeda + file contoh.
	ignores := map[string]string{".": "*.log\nbuild/\n"}
	for i, d := range dirs {
		if i%8 == 0 {
			ignores[d] = fmt.Sprintf("*.tmp\n/generated_%d/\n!keep.tmp\n", i)
		}
	}
	// Urutan kunci tetap: isi file diambil dari r, jadi iterasi map (acak) harus diurutkan.
	for _, d := range slices.Sorted(maps.Keys(ignores)) {
		pat := ignores[d]
		if err := write(filepath.Join(root, d, ".gitignore"), []byte(pat)); err != nil {
			return Stats{}, err
		}
		for _, f := range []string{"debug.log", "build/out.o", "cache.tmp", "keep.tmp"} {
			if err := write(filepath.Join(root, d, f), textFile(r, langs[10], 2000)); err != nil {
				return Stats{}, err
			}
			st.Ignored++
		}
	}
	return finish(root, st)
}

func textFile(r *rand.Rand, l lang, size int) []byte {
	var b strings.Builder
	for b.Len() < size {
		t := l.lines[r.IntN(len(l.lines))]
		args := make([]any, 0, 3)
		for _, verb := range []string{"%s", "%d"} {
			for range strings.Count(t, verb) - strings.Count(t, "%"+verb) {
				if verb == "%s" {
					args = append(args, vocab[r.IntN(len(vocab))]+"_"+vocab[r.IntN(len(vocab))])
				} else {
					args = append(args, r.IntN(100000))
				}
			}
		}
		b.WriteString(fmt.Sprintf(orderVerbs(t), args...))
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// orderVerbs mengubah templat agar semua %s dipakai sebelum %d (urutan argumen textFile).
func orderVerbs(t string) string {
	var out strings.Builder
	si, di := 0, 0
	ns := strings.Count(t, "%s")
	for i := 0; i < len(t); i++ {
		if t[i] == '%' && i+1 < len(t) {
			switch t[i+1] {
			case 's':
				si++
				fmt.Fprintf(&out, "%%[%d]s", si)
				i++
				continue
			case 'd':
				di++
				fmt.Fprintf(&out, "%%[%d]d", ns+di)
				i++
				continue
			case '%':
				out.WriteString("%%")
				i++
				continue
			}
		}
		out.WriteByte(t[i])
	}
	return out.String()
}

func write(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("gencorpus: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("gencorpus: %w", err)
	}
	return nil
}

// finish menghitung statistik + digest manifest (path berurut, slash).
func finish(root string, st Stats) (Stats, error) {
	var lines []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root {
				st.Dirs++
			}
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		lines = append(lines, hex.EncodeToString(sum[:])+"  "+filepath.ToSlash(rel)+"\n")
		st.Files++
		st.Bytes += int64(len(data))
		return nil
	})
	if err != nil {
		return Stats{}, fmt.Errorf("gencorpus: %w", err)
	}
	slices.SortFunc(lines, func(a, b string) int { return strings.Compare(a[66:], b[66:]) })
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
	}
	st.Digest = hex.EncodeToString(h.Sum(nil))
	return st, nil
}
