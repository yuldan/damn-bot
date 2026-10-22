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

// p95 of the per-iteration samples. ds is sorted in place.
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

// durableRetryWait is the pause before the CT-09 retry. The measured fsync
// stalls (185 ms, and bursts up to ~0.3 s in the phase diagnostics) are far
// shorter, so the second set sees a different disk state (ADR-127 amendment
// 2026-10-22).
const durableRetryWait = time.Second

// durableSet is one measured sample set of CT-09 (ADR-127).
type durableSet struct {
	n                    int
	gate                 time.Duration // the statistic the gate checks
	opP95, refP95        time.Duration
	pairedP50, pairedP95 time.Duration
	worst                []outlier // largest op_i − ref_i, for the log
}

// measureDurable is measure for an operation bounded by fsync (CT-09,
// ADR-127). ref, the bare durable write of the same size in a fresh directory,
// is timed alongside op in every iteration. On shared runners (perf build) the
// budget, ×1,5, applies to the p95 over iterations of the paired difference
// op_i − ref_i. A red gate is re-measured once in a fresh directory after
// durableRetryWait: a one-sided fsync stall is transient, added cost is not,
// so only two red sets fail the benchmark (ADR-127 amendment 2026-10-22).
// Elsewhere, i.e. on the reference machine Q14 at acceptance, the absolute
// p95(op) is checked once, as written.
func measureDurable(b *testing.B, name string, budget time.Duration, setup func() (op func(i int), ref func())) {
	b.Helper()
	limit := timing.Limit(budget)
	sets := durableSets(timing.SharedRunner, limit, func(label string) durableSet {
		return durableMeasure(b, name, label, setup)
	}, func(first durableSet) {
		b.Logf("%s: set 1 is red (%v > %v); re-measuring after %v in a fresh directory (ADR-127)",
			name, first.gate, limit, durableRetryWait)
		pause := time.NewTimer(durableRetryWait)
		<-pause.C
	})
	if b.N < codetoolsBudgetMinRuns {
		return // make test-timing runs one iteration: no statistic to gate
	}
	last := sets[len(sets)-1]
	b.ReportMetric(float64(last.pairedP50)/float64(time.Millisecond), "paired-p50-ms")
	b.ReportMetric(float64(last.pairedP95)/float64(time.Millisecond), "paired-p95-ms")
	b.ReportMetric(float64(last.opP95)/float64(time.Millisecond), "p95-ms")
	b.ReportMetric(float64(last.refP95)/float64(time.Millisecond), "ref-p95-ms")
	if timing.SharedRunner {
		name += " paired over the durable-write reference"
	}
	timing.Check(b, name, last.gate, budget)
}

// durableSets applies the CT-09 retry rule (ADR-127 amendment 2026-10-22): on
// a shared runner a red gate gets one fresh sample set after wait, and that
// set decides. measure is called once per set (a fresh directory each), wait
// once with the red first set. The last returned set is the verdict.
func durableSets(shared bool, limit time.Duration, measure func(label string) durableSet, wait func(first durableSet)) []durableSet {
	first := measure("set 1")
	if !needsRetry(shared, first.gate, limit, first.n) {
		return []durableSet{first}
	}
	if wait != nil {
		wait(first)
	}
	return []durableSet{first, measure("set 2")}
}

// needsRetry reports whether a red gate must be re-measured: only on a shared
// runner, and only when the set has enough samples for the statistic.
func needsRetry(shared bool, gate, limit time.Duration, n int) bool {
	return shared && n >= codetoolsBudgetMinRuns && gate > limit
}

// durableMeasure collects one sample set and logs it (both sets appear in a
// failed benchmark's output, which is the evidence ADR-127 asks for).
func durableMeasure(b *testing.B, name, label string, setup func() (op func(i int), ref func())) durableSet {
	b.Helper()
	op, ref := setup()
	ds, rs := sample(b, op, ref)
	// Everything that reads both slices happens before p95 sorts them.
	s := durableSet{
		n:         b.N,
		gate:      durableGate(ds, rs, timing.SharedRunner),
		pairedP50: pairedMedian(ds, rs),
		pairedP95: durableGate(ds, rs, true),
		worst:     pairedOutliers(ds, rs, 5),
	}
	s.opP95, s.refP95 = p95(ds), p95(rs)
	logSet(b.Logf, name, label, s)
	return s
}

// logSet writes one sample set: the gated p50/p95, both sides' p95, and the
// five iterations with the largest paired difference.
func logSet(logf func(format string, args ...any), name, label string, s durableSet) {
	logf("%s %s: n=%d samples, edit p95 %v, ref p95 %v, paired p50 %v, paired p95 %v",
		name, label, s.n, s.opP95, s.refP95, s.pairedP50, s.pairedP95)
	for _, o := range s.worst {
		logf("%s %s: paired outlier iteration %d: edit %v, ref %v, paired %v",
			name, label, o.i, o.op, o.ref, o.d)
	}
}

// pairedMedian is the median over iterations of op_i − ref_i, never negative:
// the cost edit_file adds systematically (reported next to the p95 gate).
func pairedMedian(ops, refs []time.Duration) time.Duration {
	diff := make([]time.Duration, len(ops))
	for i, d := range ops {
		diff[i] = d - refs[i]
	}
	slices.Sort(diff)
	return max(diff[len(diff)/2], 0)
}

// outlier is one iteration's samples, captured before p95 sorts the slices.
type outlier struct {
	i          int
	op, ref, d time.Duration
}

// pairedOutliers returns the n iterations with the largest op_i − ref_i,
// largest first. These are the samples the p95 paired gate is made of: a
// failing run reports where its tail came from (edit-side stall, reference
// stall, or both).
func pairedOutliers(ops, refs []time.Duration, n int) []outlier {
	out := make([]outlier, len(ops))
	for i := range ops {
		out[i] = outlier{i: i, op: ops[i], ref: refs[i], d: ops[i] - refs[i]}
	}
	slices.SortFunc(out, func(a, b outlier) int {
		switch {
		case a.d > b.d:
			return -1
		case a.d < b.d:
			return 1
		}
		return 0
	})
	return out[:min(n, len(out))]
}

// durableGate is the statistic measureDurable checks (ADR-127): on shared
// runners p95_i(op_i − ref_i), never negative, where op_i and ref_i were
// timed in the same iteration (sample appends both in lockstep); otherwise
// p95(op). Neither input is modified, so the pairing survives for later use.
func durableGate(ops, refs []time.Duration, shared bool) time.Duration {
	if !shared {
		return p95(slices.Clone(ops))
	}
	diff := make([]time.Duration, len(ops))
	for i, d := range ops {
		diff[i] = d - refs[i]
	}
	return max(p95(diff), 0)
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

// samples is the perf job's codetoolsBudgetMinRuns per-iteration durations
// of base each, with extra added to the iterations it names (a disk stall,
// or a regression's cost). p95 of them is then the 95th smallest.
func samples(base time.Duration, extra map[int]time.Duration) []time.Duration {
	ds := make([]time.Duration, codetoolsBudgetMinRuns)
	for i := range ds {
		ds[i] = base + extra[i]
	}
	return ds
}

// TestDurableGate: on shared runners CT-09 is p95 over iterations of
// edit_i − ref_i (never negative), on Q14 the absolute p95(edit).
func TestDurableGate(t *testing.T) {
	const n, ms = codetoolsBudgetMinRuns, time.Millisecond
	budget := 30 * ms // CT-09 20 ms × 1,5 in the perf build
	stall := func(from, count int, d time.Duration) map[int]time.Duration {
		m := make(map[int]time.Duration, count)
		for i := from; i < from+count; i++ {
			m[i] = d
		}
		return m
	}
	// varying is a healthy reference whose latency drifts per iteration;
	// healthy edits cost it plus 5 ms in the same iteration.
	varying, healthy := make([]time.Duration, n), make([]time.Duration, n)
	for i := range n {
		varying[i] = time.Duration(2+i%7) * ms
		healthy[i] = varying[i] + 5*ms
	}
	for _, c := range []struct {
		name      string
		ops, refs []time.Duration
		shared    bool
		want      time.Duration
		pass      bool
	}{
		{"drifting disk cancels per iteration", healthy, varying, true, 5 * ms, true},
		{
			"stall hitting both samples of an iteration cancels",
			samples(15*ms, stall(10, 10, 200*ms)), samples(10*ms, stall(10, 10, 200*ms)), true, 5 * ms, true,
		},
		// p95(edit) − p95(ref) would be 50 − 210 → 0 here and pass a
		// +40 ms regression; pairing keeps the 40 ms of the other 94.
		{
			"reference-only burst does not hide a regression",
			samples(50*ms, nil), samples(10*ms, stall(0, 6, 200*ms)), true, 40 * ms, false,
		},
		// Unrelated stalls of equal size on each side: unpaired p95s would
		// cancel them (215 − 210 = 5 ms) though no iteration's edit was
		// matched by its own reference.
		{
			"edit stalls not offset by reference stalls in other iterations",
			samples(15*ms, stall(0, 6, 200*ms)), samples(10*ms, stall(50, 6, 200*ms)), true, 205 * ms, false,
		},
		{
			"edit-only stalls in 5 % of iterations are p95 tail",
			samples(15*ms, stall(0, 5, 200*ms)), samples(10*ms, nil), true, 5 * ms, true,
		},
		{
			"edit-only stalls in 6 % of iterations reach p95",
			samples(15*ms, stall(0, 6, 200*ms)), samples(10*ms, nil), true, 205 * ms, false,
		},
		{"edit faster than the reference clamps to zero", samples(5*ms, nil), samples(8*ms, nil), true, 0, true},
		{"regression at the threshold passes", samples(40*ms, nil), samples(10*ms, nil), true, 30 * ms, true},
		{
			"Q14: absolute p95, reference ignored",
			samples(25*ms, stall(0, 6, 5*ms)), samples(20*ms, stall(0, 50, 100*ms)), false, 30 * ms, true,
		},
		{"Q14: edit over budget fails whatever the reference", samples(31*ms, nil), samples(31*ms, nil), false, 31 * ms, false},
	} {
		ops, refs := slices.Clone(c.ops), slices.Clone(c.refs)
		got := durableGate(c.ops, c.refs, c.shared)
		if got != c.want {
			t.Errorf("%s: durableGate = %v, want %v", c.name, got, c.want)
		}
		if pass := got <= budget; pass != c.pass {
			t.Errorf("%s: gate %v ≤ %v is %v, want %v", c.name, got, budget, pass, c.pass)
		}
		// measureDurable reports p95 of each side after the gate; the
		// gate must not reorder the samples it pairs.
		if !slices.Equal(c.ops, ops) || !slices.Equal(c.refs, refs) {
			t.Errorf("%s: durableGate modified its samples", c.name)
		}
	}
}

// TestDurableRetry pins the CT-09 retry rule (ADR-127 amendment 2026-10-22):
// a red first set is re-measured once, in a fresh directory, after the wait;
// the second set decides. A regression that fails both sets still fails.
func TestDurableRetry(t *testing.T) {
	const ms = time.Millisecond
	limit := 30 * ms
	set := func(gate time.Duration) durableSet {
		return durableSet{n: codetoolsBudgetMinRuns, gate: gate, pairedP95: gate}
	}
	seq := func(sets ...durableSet) (func(string) durableSet, *[]string) {
		i := 0
		labels := &[]string{}
		return func(label string) durableSet {
			*labels = append(*labels, label)
			s := sets[i]
			i++
			return s
		}, labels
	}
	for _, c := range []struct {
		name     string
		shared   bool
		sets     []durableSet
		wantSets int
		wantPass bool
	}{
		{"first set red, second green: overall pass", true, []durableSet{set(49 * ms), set(6 * ms)}, 2, true},
		{"both sets red: overall fail", true, []durableSet{set(49 * ms), set(60 * ms)}, 2, false},
		{"+35 ms regression fails both sets", true, []durableSet{set(35 * ms), set(35 * ms)}, 2, false},
		{"green first set: no retry", true, []durableSet{set(6 * ms)}, 1, true},
		{"Q14 (not shared): one set, no retry", false, []durableSet{set(49 * ms)}, 1, false},
	} {
		measure, labels := seq(c.sets...)
		waits := 0
		got := durableSets(c.shared, limit, measure, func(durableSet) { waits++ })
		if len(got) != c.wantSets || len(*labels) != c.wantSets {
			t.Fatalf("%s: %d sets %v, want %d", c.name, len(got), *labels, c.wantSets)
		}
		verdict := got[len(got)-1]
		if pass := verdict.gate <= limit; pass != c.wantPass {
			t.Errorf("%s: verdict gate %v ≤ %v is %v, want %v", c.name, verdict.gate, limit, pass, c.wantPass)
		}
		if wantWait := c.wantSets - 1; waits != wantWait {
			t.Errorf("%s: wait called %d times, want %d", c.name, waits, wantWait)
		}
		if c.wantSets == 2 && (*labels)[1] != "set 2" {
			t.Errorf("%s: second label %q, want set 2", c.name, (*labels)[1])
		}
	}
	if needsRetry(true, 49*ms, limit, codetoolsBudgetMinRuns-1) {
		t.Error("needsRetry with too few samples: want false")
	}
}

// TestDurableSetsLogged: both sample sets are logged with the gated p50/p95,
// both sides' p95 and the top-5 paired outliers - the evidence a red perf run
// must carry (ADR-127).
func TestDurableSetsLogged(t *testing.T) {
	const ms = time.Millisecond
	var lines []string
	logf := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	red := durableSet{
		n: 100, gate: 49 * ms, opP95: 50 * ms, refP95: time.Millisecond,
		pairedP50: 5 * ms, pairedP95: 49 * ms,
		worst: []outlier{{i: 7, op: 50 * ms, ref: time.Millisecond, d: 49 * ms}},
	}
	green := durableSet{n: 100, gate: 6 * ms, opP95: 8 * ms, refP95: 2 * ms, pairedP50: 5 * ms, pairedP95: 6 * ms}
	logSet(logf, "CT-09", "set 1", red)
	logSet(logf, "CT-09", "set 2", green)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"set 1: n=100", "set 1: paired outlier iteration 7",
		"set 2: n=100",
		"edit p95 50ms", "ref p95 1ms", "paired p50 5ms", "paired p95 49ms",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("log missing %q:\n%s", want, joined)
		}
	}
}

// spin busy-waits d: a sample of known minimum length without time.Sleep
// (E-30/E-72).
func spin(d time.Duration) {
	for t0 := time.Now(); time.Since(t0) < d; {
	}
}

// setBenchtime sets -test.benchtime for testing.Benchmark until cleanup.
func setBenchtime(t *testing.T, v string) {
	t.Helper()
	bt := flag.Lookup("test.benchtime")
	old := bt.Value.String()
	t.Cleanup(func() {
		if err := bt.Value.Set(old); err != nil {
			t.Error(err)
		}
	})
	if err := bt.Value.Set(v); err != nil {
		t.Fatal(err)
	}
}

// TestMeasureBudgetNeedsPerfSamples: the single run per benchmark of make
// test-timing never asserts a p95 budget, make perf's 100 samples do
// (ADR-073 AUD-035(1)). The enforced run also fails under -race, where
// timing refuses enforcement (ADR-048), so the expectation holds in both.
func TestMeasureBudgetNeedsPerfSamples(t *testing.T) {
	t.Setenv(timing.Env, "1")
	for _, c := range []struct {
		benchtime string
		enforced  bool
	}{{"1x", false}, {fmt.Sprintf("%dx", codetoolsBudgetMinRuns-1), false}, {fmt.Sprintf("%dx", codetoolsBudgetMinRuns), true}} {
		setBenchtime(t, c.benchtime)
		// Every gated statistic (≥ 0, the paired difference clamped even
		// when the reference is the slower side) exceeds a negative
		// budget; testing.Benchmark returns a zero result when the
		// benchmark fails.
		for name, f := range map[string]func(*testing.B){
			"measure": func(b *testing.B) { measure(b, "min runs", -1, func(int) {}) },
			"measureDurable": func(b *testing.B) {
				measureDurable(b, "min runs", -1, func() (func(int), func()) {
					return func(int) {}, func() {}
				})
			},
			"measureDurable slow reference": func(b *testing.B) {
				measureDurable(b, "min runs", -1, func() (func(int), func()) {
					return func(int) {}, func() { spin(50 * time.Microsecond) }
				})
			},
		} {
			if enforced := testing.Benchmark(f).N == 0; enforced != c.enforced {
				t.Errorf("%s -benchtime=%s: budget enforced = %v, want %v", name, c.benchtime, enforced, c.enforced)
			}
		}
	}
}

// TestMeasureDurableMetrics: from codetoolsBudgetMinRuns samples on,
// measureDurable reports the paired statistic next to both sides' p95, and
// since ref_i ≥ 0 the paired p95 never exceeds the edit's own p95.
func TestMeasureDurableMetrics(t *testing.T) {
	t.Setenv(timing.Env, "") // report only: the budget itself is TestMeasureBudgetNeedsPerfSamples'
	for _, runs := range []int{codetoolsBudgetMinRuns - 1, codetoolsBudgetMinRuns} {
		setBenchtime(t, fmt.Sprintf("%dx", runs))
		r := testing.Benchmark(func(b *testing.B) {
			measureDurable(b, "metrics", time.Hour, func() (func(int), func()) {
				return func(int) { spin(100 * time.Microsecond) }, func() { spin(50 * time.Microsecond) }
			})
		})
		paired, okPaired := r.Extra["paired-p95-ms"]
		op, okOp := r.Extra["p95-ms"]
		ref, okRef := r.Extra["ref-p95-ms"]
		if reported := okPaired && okOp && okRef; reported != (runs >= codetoolsBudgetMinRuns) {
			t.Fatalf("%d runs: metrics %v, want reported = %v", runs, r.Extra, runs >= codetoolsBudgetMinRuns)
		}
		if runs < codetoolsBudgetMinRuns {
			continue
		}
		if ref < 0.05 || op < 0.1 || paired < 0 || paired > op {
			t.Errorf("metrics paired %v, edit %v, ref %v ms: want ref ≥ 0,05, edit ≥ 0,1, 0 ≤ paired ≤ edit", paired, op, ref)
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
		// Each sample set gets a fresh directory and engine: the retry must
		// not reuse the dirty pages or journal of the set that failed
		// (ADR-127 amendment 2026-10-22).
		measureDurable(b, "CT-09 edit 1 MB", 20*time.Millisecond, func() (func(int), func()) {
			v := newEnv(b)
			content := strings.Repeat(strings.Repeat("e", 99)+"\n", 10<<10) + "marker 0\n"
			v.put(b, "e.txt", content)
			tag := v.tag(b, "e.txt") //nolint:contextcheck // setup read with a background context
			ref := durableWrite(b, v.root, []byte(content))
			return func(i int) {
				r, err := v.e.Edit(ctx, EditArgs{Path: "e.txt", Tag: tag, Edits: []EditItem{{
					OldString: new(fmt.Sprintf("marker %d\n", i)), NewString: new(fmt.Sprintf("marker %d\n", i+1)),
				}}}, Opts{})
				if err != nil {
					b.Fatal(err)
				}
				tag = metaStr(&r, "tag")
			}, ref
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
