package codetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/testutil/timing"
)

// p95 of the per-iteration samples.
func p95(ds []time.Duration) time.Duration {
	slices.Sort(ds)
	return ds[(len(ds)*95+99)/100-1]
}

// measure runs op b.N times, then checks the p95 budget (ADR-048, ADR-077:
// enforced only in test-timing/perf builds).
func measure(b *testing.B, name string, budget time.Duration, op func(i int)) {
	b.Helper()
	ds := make([]time.Duration, 0, b.N)
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		t0 := time.Now()
		op(i)
		ds = append(ds, time.Since(t0))
	}
	timing.Check(b, name, p95(ds), budget)
}

// c3Like builds a corpus shaped like C3 (CODE-TOOLS §10.1: 1.000 files,
// ≈ 12 MB, 12 languages, .gitignore, 1% binary); the real C3 runs in
// bench/codetools.
func c3Like(b *testing.B, v *env) {
	rng := testRand(3)
	words := []string{"parse", "render", "handler", "config", "session", "token", "buffer", "stream"}
	exts := []string{".go", ".py", ".js", ".ts", ".rs", ".java", ".c", ".cpp", ".rb", ".sh", ".md", ".yaml"}
	v.put(b, ".gitignore", "*.tmp\nbuild/\n")
	for i := range 1000 {
		var s strings.Builder
		for s.Len() < 11000+rng.IntN(2000) {
			fmt.Fprintf(&s, "func %s_%d(ctx context.Context) error { return %s(%d) }\n", words[rng.IntN(8)], rng.IntN(500), words[rng.IntN(8)], i)
		}
		name := fmt.Sprintf("%s_%d/%s%s", words[i%8], i%40, words[rng.IntN(8)], exts[i%12])
		if i%100 == 7 {
			s.WriteString("\x00binary")
		}
		v.put(b, fmt.Sprintf("%03d_%s", i, name), s.String())
	}
}

// BenchmarkCodetools holds the absolute budgets of CODE-TOOLS §10.2
// enforced by the perf job (ADR-094 butir 5): CT-04, CT-05, CT-06, CT-07,
// CT-09.
func BenchmarkCodetools(b *testing.B) {
	ctx := context.Background()
	b.Run("GrepToolC3", func(b *testing.B) { // CT-04: p95 ≤ 15 ms
		v := newEnv(b)
		c3Like(b, v)
		queries := []string{"parse", "handler_1", `token_\d+\(`, "session", "zzz_absent"}
		measure(b, "CT-04 grep C3", 15*time.Millisecond, func(i int) {
			if _, err := v.e.Grep(ctx, &GrepArgs{Pattern: queries[i%len(queries)]}, Opts{}); err != nil {
				b.Fatal(err)
			}
		})
	})
	b.Run("ReadPage1MB", func(b *testing.B) { // CT-05: p95 ≤ 3 ms
		v := newEnv(b)
		v.put(b, "one.txt", strings.Repeat(strings.Repeat("r", 99)+"\n", 10<<10))
		rng := testRand(5)
		measure(b, "CT-05 read 1 MB page", 3*time.Millisecond, func(int) {
			if _, err := v.e.Read(ctx, ReadArgs{Path: "one.txt", Offset: new(1 + rng.IntN(10<<10-400)), Limit: 400}, Opts{}); err != nil {
				b.Fatal(err)
			}
		})
	})
	b.Run("ReadTail100MB", func(b *testing.B) { // CT-06: p95 ≤ 10 ms, cold index
		v := newEnv(b)
		p := filepath.Join(v.root, "big.log")
		bigFile(b, p, 100<<20)
		measure(b, "CT-06 tail 100 MB", 10*time.Millisecond, func(int) {
			e, err := New(&Config{Sandbox: v.sb, Roots: []string{v.root}, Home: v.home})
			if err != nil {
				b.Fatal(err)
			}
			defer e.Close()
			if _, err := e.Read(ctx, ReadArgs{Path: "big.log", Offset: new(-100)}, Opts{}); err != nil {
				b.Fatal(err)
			}
		})
	})
	b.Run("OutlineGo1MB", func(b *testing.B) { // CT-07: p95 ≤ 50 ms
		v := newEnv(b)
		var s strings.Builder
		s.WriteString("package big\n")
		for i := 0; s.Len() < 1<<20; i++ {
			fmt.Fprintf(&s, "\n// F%d does work.\nfunc F%d(x int) int {\n\tif x > %d {\n\t\treturn x\n\t}\n\treturn F%d(x + 1)\n}\n\ntype T%d struct{ a, b int }\n", i, i, i, i+1, i)
		}
		v.put(b, "big.go", s.String())
		measure(b, "CT-07 outline Go 1 MB", 50*time.Millisecond, func(int) {
			if _, err := v.e.Read(ctx, ReadArgs{Path: "big.go", Outline: true}, Opts{}); err != nil {
				b.Fatal(err)
			}
		})
	})
	b.Run("Edit1MB", func(b *testing.B) { // CT-09: p95 ≤ 20 ms incl. fsync file + dir
		v := newEnv(b)
		v.put(b, "e.txt", strings.Repeat(strings.Repeat("e", 99)+"\n", 10<<10)+"marker 0\n")
		tag := v.tag(b, "e.txt") //nolint:contextcheck // setup read with a background context
		measure(b, "CT-09 edit 1 MB", 20*time.Millisecond, func(i int) {
			r, err := v.e.Edit(ctx, EditArgs{Path: "e.txt", Tag: tag, Edits: []EditItem{{
				OldString: new(fmt.Sprintf("marker %d\n", i)), NewString: new(fmt.Sprintf("marker %d\n", i+1)),
			}}}, Opts{})
			if err != nil {
				b.Fatal(err)
			}
			tag = metaStr(&r, "tag")
		})
	})
}

// bigFile writes size bytes of 80-char numbered lines without holding them.
func bigFile(b *testing.B, p string, size int) {
	f, err := os.Create(p) //nolint:gosec // G304: benchmark temp file
	if err != nil {
		b.Fatal(err)
	}
	line := []byte(strings.Repeat("t", 70) + " 0000000\n")
	for n := 0; n < size; n += len(line) {
		copy(line[71:78], fmt.Sprintf("%07d", n/len(line)))
		if _, err := f.Write(line); err != nil {
			b.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		b.Fatal(err)
	}
}
