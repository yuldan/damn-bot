package exec

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/testutil/timing"
)

// spawnBudgetMinRuns: make test-timing runs every benchmark once; the p95
// gate needs a real sample (make perf runs -benchtime=100x).
const spawnBudgetMinRuns = 100

// trueArgv is the no-op command of §A.9: /bin/true, `cmd /c exit 0`.
func trueArgv(b *testing.B) []string {
	b.Helper()
	if runtime.GOOS == "windows" {
		p, err := exec.LookPath("cmd")
		if err != nil {
			b.Skip("no cmd.exe")
		}
		return []string{p, "/d", "/c", "exit 0"}
	}
	p, err := exec.LookPath("true")
	if err != nil {
		b.Skip("no true")
	}
	return []string{p}
}

// p95 of ds (sorted in place).
func p95(ds []time.Duration) time.Duration {
	slices.Sort(ds)
	return ds[(len(ds)*95+99)/100-1]
}

// directRun is the baseline: the same program started and reaped without
// the runner (§A.9 subtracts it).
func directRun(b *testing.B, argv []string) time.Duration {
	b.Helper()
	ds := make([]time.Duration, 0, 50)
	for range 50 {
		t0 := time.Now()
		p, err := os.StartProcess(argv[0], argv, &os.ProcAttr{Files: []*os.File{nil, nil, nil}})
		if err != nil {
			b.Fatal(err)
		}
		if _, err := p.Wait(); err != nil {
			b.Fatal(err)
		}
		ds = append(ds, time.Since(t0))
	}
	slices.Sort(ds)
	return ds[len(ds)/2]
}

// benchSpawn measures Run of the no-op command and gates the p95 overhead.
func benchSpawn(b *testing.B, pty bool, budget time.Duration, name string) {
	argv := trueArgv(b)
	base := directRun(b, argv)
	r := New(context.Background())
	b.Cleanup(func() {
		if err := r.Close(); err != nil {
			b.Error(err)
		}
	})
	env := (&EnvPolicy{}).Env(os.Environ(), pty, "", "", "")
	ds := make([]time.Duration, 0, b.N)
	for b.Loop() {
		s := Spec{Argv: argv, Dir: os.TempDir(), Env: env, PTY: pty}
		t0 := time.Now()
		res, err := r.Run(context.Background(), &s)
		ds = append(ds, time.Since(t0))
		if err != nil || res.Status != StatusExited {
			b.Fatal(err, res.Status)
		}
	}
	if b.N >= spawnBudgetMinRuns {
		timing.Check(b, name, max(p95(ds)-base, 0), budget)
	}
}

// BenchmarkShellSpawnPipe (§A.9, P24.3): p95 spawn overhead < 15 ms.
func BenchmarkShellSpawnPipe(b *testing.B) {
	if runtime.GOOS == "windows" {
		b.Skip("BenchmarkShellSpawnWindows")
	}
	benchSpawn(b, false, 15*time.Millisecond, "shell spawn overhead p95, pipe (§A.9)")
}

// BenchmarkShellSpawnPTY (§A.9, P24.3): p95 spawn overhead < 15 ms.
func BenchmarkShellSpawnPTY(b *testing.B) {
	if runtime.GOOS == "windows" {
		b.Skip("BenchmarkShellSpawnWindows")
	}
	benchSpawn(b, true, 15*time.Millisecond, "shell spawn overhead p95, pty (§A.9)")
}

// BenchmarkShellSpawnWindows (§A.9, P24.3): p95 < 40 ms, pipe (ConPTY in
// its own PR).
func BenchmarkShellSpawnWindows(b *testing.B) {
	if runtime.GOOS != "windows" {
		b.Skip("windows only")
	}
	benchSpawn(b, false, 40*time.Millisecond, "shell spawn overhead p95, windows pipe (§A.9)")
}
