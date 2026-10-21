package codetools

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/testutil/timing"
)

// codetoolsBudgetMinRuns: make test-timing runs every benchmark once
// (-benchtime=1x) only so it executes without -race; a p95 budget needs a
// real sample, which make perf provides (-benchtime=100x, ADR-073
// AUD-035(1)).
const codetoolsBudgetMinRuns = 100

// p95 of the per-iteration samples.
func p95(ds []time.Duration) time.Duration {
	slices.Sort(ds)
	return ds[(len(ds)*95+99)/100-1]
}

// measure runs op b.N times and, from codetoolsBudgetMinRuns samples on,
// reports and checks their p95 (ADR-048, ADR-077: asserted only under
// DAMNBOT_TIMING=1, ×1,5 in the perf build).
func measure(b *testing.B, name string, budget time.Duration, op func(i int)) {
	b.Helper()
	ds, _ := sample(b, op, nil)
	if b.N < codetoolsBudgetMinRuns {
		return
	}
	p := p95(ds)
	b.ReportMetric(float64(p)/float64(time.Millisecond), "p95-ms") // the gated statistic, not only the mean ns/op
	timing.Check(b, name, p, budget)
}

// measureDurable is measure for an operation bounded by fsync (CT-09,
// ADR-127). ref, the bare durable write of the same size in the same
// directory, is timed alongside op in every iteration. On shared runners
// (perf build) the budget, ×1,5, applies to p95(op) − p95(ref): the shared
// disk's latency cancels out, the cost edit_file adds on top of it does
// not. Elsewhere, i.e. on the reference machine Q14 at acceptance, the
// absolute p95(op) is checked as written.
func measureDurable(b *testing.B, name string, budget time.Duration, op func(i int), ref func()) {
	b.Helper()
	ds, rs := sample(b, op, ref)
	if b.N < codetoolsBudgetMinRuns {
		return
	}
	p, r := p95(ds), p95(rs)
	b.ReportMetric(float64(p)/float64(time.Millisecond), "p95-ms")
	b.ReportMetric(float64(r)/float64(time.Millisecond), "ref-p95-ms")
	if timing.SharedRunner {
		name += " over the durable-write reference"
	}
	timing.Check(b, name, durableGate(p, r, timing.SharedRunner), budget)
}

// durableGate is the statistic measureDurable checks (ADR-127).
func durableGate(p95Op, p95Ref time.Duration, shared bool) time.Duration {
	if !shared {
		return p95Op
	}
	return max(p95Op-p95Ref, 0)
}

// sample runs op b.N times after one warm-up outside the timer, so every
// sample is steady state. A non-nil ref is warmed and timed once per
// iteration too, outside ns/op, alternating which of the two runs first so
// neither always meets the other's dirty pages or journal commit.
func sample(b *testing.B, op func(i int), ref func()) (ds, rs []time.Duration) {
	b.Helper()
	op(0)
	if ref != nil {
		ref()
	}
	ds = make([]time.Duration, 0, b.N)
	rs = make([]time.Duration, 0, b.N)
	timeRef := func() {
		b.StopTimer()
		t0 := time.Now()
		ref()
		rs = append(rs, time.Since(t0))
		b.StartTimer()
	}
	b.ResetTimer()
	for i := 1; b.Loop(); i++ {
		if ref != nil && i%2 == 0 {
			timeRef()
		}
		t0 := time.Now()
		op(i)
		ds = append(ds, time.Since(t0))
		if ref != nil && i%2 == 1 {
			timeRef()
		}
	}
	return ds, rs
}

// durableWrite is the CT-09 reference (ADR-127): the bare durable replace of
// data in dir with the engine's own primitives (temp write, fsync(file),
// rename, fsync(dir) where the platform has it) and none of edit_file's
// work (read, match, diff, hash, mode/xattr copy, read-back).
func durableWrite(b *testing.B, dir string, data []byte) func() {
	dst := filepath.Join(dir, "ref.dat")
	return func() {
		f, err := os.CreateTemp(dir, ".ref-*")
		if err != nil {
			b.Fatal(err)
		}
		_, err = f.Write(data)
		if err = errors.Join(err, f.Sync(), f.Close()); err == nil {
			err = renameReplace(f.Name(), dst)
		}
		if err == nil && dirSyncable {
			err = fsyncPath(dir)
		}
		if err != nil {
			b.Fatal(err)
		}
	}
}

// TestDurableGate: on shared runners CT-09 is the p95 excess over the
// durable-write reference (never negative), on Q14 the absolute p95.
func TestDurableGate(t *testing.T) {
	ms := time.Millisecond
	for _, c := range []struct {
		op, ref time.Duration
		shared  bool
		want    time.Duration
	}{
		{25 * ms, 20 * ms, true, 5 * ms},
		{25 * ms, 30 * ms, true, 0},
		{25 * ms, 20 * ms, false, 25 * ms},
	} {
		if got := durableGate(c.op, c.ref, c.shared); got != c.want {
			t.Errorf("durableGate(%v, %v, shared=%v) = %v, want %v", c.op, c.ref, c.shared, got, c.want)
		}
	}
}

// TestMeasureBudgetNeedsPerfSamples: the single run per benchmark of make
// test-timing never asserts a p95 budget, make perf's 100 samples do
// (ADR-073 AUD-035(1)). The enforced run also fails under -race, where
// timing refuses enforcement (ADR-048), so the expectation holds in both.
func TestMeasureBudgetNeedsPerfSamples(t *testing.T) {
	t.Setenv(timing.Env, "1")
	bt := flag.Lookup("test.benchtime")
	old := bt.Value.String()
	t.Cleanup(func() {
		if err := bt.Value.Set(old); err != nil {
			t.Error(err)
		}
	})
	for _, c := range []struct {
		benchtime string
		enforced  bool
	}{{"1x", false}, {fmt.Sprintf("%dx", codetoolsBudgetMinRuns-1), false}, {fmt.Sprintf("%dx", codetoolsBudgetMinRuns), true}} {
		if err := bt.Value.Set(c.benchtime); err != nil {
			t.Fatal(err)
		}
		// Every gated statistic (≥ 0) exceeds a negative budget;
		// testing.Benchmark returns a zero result when the benchmark fails.
		for name, f := range map[string]func(*testing.B){
			"measure":        func(b *testing.B) { measure(b, "min runs", -1, func(int) {}) },
			"measureDurable": func(b *testing.B) { measureDurable(b, "min runs", -1, func(int) {}, func() {}) },
		} {
			if enforced := testing.Benchmark(f).N == 0; enforced != c.enforced {
				t.Errorf("%s -benchtime=%s: budget enforced = %v, want %v", name, c.benchtime, enforced, c.enforced)
			}
		}
	}
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

// BenchmarkCodetools holds the budgets of CODE-TOOLS §10.2 enforced by the
// perf job (ADR-094 butir 5): CT-04, CT-05, CT-06, CT-07 absolute (×1,5),
// CT-09 relative to a durable-write reference on shared runners and
// absolute on Q14 (ADR-127).
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
		content := strings.Repeat(strings.Repeat("e", 99)+"\n", 10<<10) + "marker 0\n"
		v.put(b, "e.txt", content)
		tag := v.tag(b, "e.txt") //nolint:contextcheck // setup read with a background context
		ref := durableWrite(b, v.root, []byte(content))
		measureDurable(b, "CT-09 edit 1 MB", 20*time.Millisecond, func(i int) {
			r, err := v.e.Edit(ctx, EditArgs{Path: "e.txt", Tag: tag, Edits: []EditItem{{
				OldString: new(fmt.Sprintf("marker %d\n", i)), NewString: new(fmt.Sprintf("marker %d\n", i+1)),
			}}}, Opts{})
			if err != nil {
				b.Fatal(err)
			}
			tag = metaStr(&r, "tag")
		}, ref)
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
