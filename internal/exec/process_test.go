//go:build unix && !minimal

package exec

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// trueCommand resolves the platform's true(1) (macOS keeps it in /usr/bin).
func trueCommand(t *testing.T) []string {
	t.Helper()
	p, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("true(1) not found: %v", err)
	}
	return []string{p}
}

func bgStart(t *testing.T, b *Background, session, script string) ProcessInfo {
	t.Helper()
	info, err := b.Start(t.Context(), session, []string{"/bin/sh", "-c", script}, "/tmp",
		[]string{"PATH=/usr/bin:/bin"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return info
}

func TestBackgroundOutputAndFinalStatus(t *testing.T) {
	t.Parallel()
	b := NewBackground()
	t.Cleanup(func() {
		//nolint:errcheck,gosec // test cleanup
		b.Close(context.Background())
	})
	info := bgStart(t, b, "s1", "printf 'one\\ntwo\\n'; sleep 0.1; printf 'three\\n'")
	var pr PollResult
	var err error
	for range 100 {
		pr, err = b.Poll("s1", info.ID, 0, 0, "")
		if err == nil && len(pr.Output) > 0 {
			break
		}
		<-time.After(5 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if !strings.Contains(string(pr.Output), "one") || pr.Offset == 0 {
		t.Fatalf("poll = %+v", pr)
	}
	final, still, err := b.Wait(t.Context(), "s1", info.ID, 5*time.Second)
	if err != nil || still {
		t.Fatalf("Wait still=%v err=%v", still, err)
	}
	if final.Status != ProcessExited || final.ExitCode == nil || *final.ExitCode != 0 {
		t.Fatalf("final = %+v", final)
	}
	pr, err = b.Poll("s1", info.ID, 0, 2, "three")
	if err != nil || !strings.Contains(string(pr.Output), "three") || strings.Contains(string(pr.Output), "one") {
		t.Fatalf("grep/tail = %+v err=%v", pr, err)
	}
}

func TestBackgroundSessionIsolation(t *testing.T) {
	t.Parallel()
	b := NewBackground()
	t.Cleanup(func() {
		//nolint:errcheck,gosec // test cleanup
		b.Close(context.Background())
	})
	info := bgStart(t, b, "owner", "sleep 30")
	if _, err := b.Kill(t.Context(), "other", info.ID, "KILL"); !errors.Is(err, ErrProcessNotFound) {
		t.Fatalf("foreign kill = %v", err)
	}
	if _, _, err := b.Wait(t.Context(), "other", info.ID, time.Second); !errors.Is(err, ErrProcessNotFound) {
		t.Fatalf("foreign wait = %v", err)
	}
	pr, err := b.Poll("owner", info.ID, 0, 0, "")
	if err != nil || pr.Info.Status != ProcessRunning {
		t.Fatalf("owner poll = %+v err=%v", pr, err)
	}
}

func TestBackgroundCapsAndKillFinal(t *testing.T) {
	t.Parallel()
	b := NewBackground()
	t.Cleanup(func() {
		//nolint:errcheck,gosec // test cleanup
		b.Close(context.Background())
	})
	var ids []string
	for range MaxLivePerSession {
		info := bgStart(t, b, "s", "sleep 30")
		ids = append(ids, info.ID)
	}
	if _, err := b.Start(t.Context(), "s", trueCommand(t), "/tmp", nil); !errors.Is(err, ErrProcessLimit) {
		t.Fatalf("cap = %v", err)
	}
	final, err := b.Kill(t.Context(), "s", ids[0], "TERM")
	if err != nil || final.Status == ProcessRunning {
		t.Fatalf("kill = %+v err=%v", final, err)
	}
	// After exit the slot is free.
	if _, err := b.Start(t.Context(), "s", trueCommand(t), "/tmp", nil); err != nil {
		t.Fatalf("start after kill: %v", err)
	}
}

func TestBackgroundWriteAndClose(t *testing.T) {
	t.Parallel()
	b := NewBackground()
	info := bgStart(t, b, "s", "read x; printf 'got:%s' \"$x\"")
	if err := b.Write("s", info.ID, "hello", true); err != nil {
		t.Fatalf("Write: %v", err)
	}
	final, still, err := b.Wait(t.Context(), "s", info.ID, 5*time.Second)
	if err != nil || still || final.ExitCode == nil {
		t.Fatalf("Wait still=%v err=%v final=%+v", still, err, final)
	}
	pr, perr := b.Poll("s", info.ID, 0, 0, "")
	if perr != nil {
		t.Fatalf("Poll: %v", perr)
	}
	if !strings.Contains(string(pr.Output), "got:hello") {
		t.Fatalf("output = %q", pr.Output)
	}
	if err := b.Write("s", info.ID, "x", true); err == nil {
		t.Fatal("write after exit: want error")
	}
	// Close kills a live process; a new registry starts empty (no restart
	// survival).
	live := bgStart(t, b, "s2", "sleep 30")
	if err := b.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, _, err := b.Wait(context.Background(), "s2", live.ID, time.Second); !errors.Is(err, ErrProcessClosed) {
		t.Fatalf("wait after close = %v", err)
	}
	b2 := NewBackground()
	t.Cleanup(func() {
		//nolint:errcheck,gosec // test cleanup
		b2.Close(context.Background())
	})
	if got := b2.List("s2"); len(got) != 0 {
		t.Fatalf("restart registry = %+v", got)
	}
}

// TestBackgroundSpawnRace starts and kills 200 processes under -race.
func TestBackgroundSpawnRace(t *testing.T) {
	t.Parallel()
	b := NewBackground()
	t.Cleanup(func() {
		//nolint:errcheck,gosec // test cleanup
		b.Close(context.Background())
	})
	for i := range 200 {
		session := fmt.Sprintf("s%d", i%4)
		info, err := b.Start(t.Context(), session, []string{"/bin/sh", "-c", "sleep 0.05"}, "/tmp", []string{"PATH=/bin:/usr/bin"})
		if err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
		if _, err := b.Kill(t.Context(), session, info.ID, "KILL"); err != nil {
			t.Fatalf("kill %d: %v", i, err)
		}
	}
	if got := len(b.List("s0")) + len(b.List("s1")) + len(b.List("s2")) + len(b.List("s3")); got == 0 {
		t.Fatal("no entries")
	}
}

func TestRingBoundedAndOffsets(t *testing.T) {
	t.Parallel()
	r := newRing(16)
	if _, err := r.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Write([]byte("abcdefghij")); err != nil {
		t.Fatal(err)
	}
	data, next := r.bytesSince(0)
	if string(data) != "456789abcdefghij" || next != 20 {
		t.Fatalf("ring = %q next=%d", data, next)
	}
	data, _ = r.bytesSince(18)
	if string(data) != "ij" {
		t.Fatalf("since = %q", data)
	}
}

func TestPruneFinishedRespectsRetention(t *testing.T) {
	t.Parallel()
	b := NewBackground()
	now := time.Now()
	b.now = func() time.Time { return now }
	info := bgStart(t, b, "s", "true")
	final, still, err := b.Wait(t.Context(), "s", info.ID, 5*time.Second)
	if err != nil || still || final.Status != ProcessExited {
		t.Fatalf("wait = %+v still=%v err=%v", final, still, err)
	}
	now = now.Add(ProcessRetention + time.Minute)
	if got := b.List("s"); len(got) != 0 {
		t.Fatalf("retention prune = %+v", got)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { b.List("s") })
	}
	wg.Wait()
}
