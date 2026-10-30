package cron

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
)

// hashStore is the module_state seam for monitor hashes.
type hashStore map[string]string

func (h hashStore) GateHash(_ context.Context, id string) (string, bool, error) {
	v, ok := h[id]
	return v, ok, nil
}

func (h hashStore) SetGateHash(_ context.Context, id, hash string) error {
	h[id] = hash
	return nil
}

func TestParseGate(t *testing.T) {
	wake := false
	for name, tc := range map[string]struct {
		raw     string
		wantErr bool
		wake    *bool
	}{
		"empty":   {"", false, nil},
		"verdict": {`{"wake":false,"context":"x"}`, false, &wake},
		"monitor": {`{"monitor":{"url":"https://example.test/a","selector":".body","ignore":["t"]}}`, false, nil},
		"two":     {`{"wake":true,"script":["true"]}`, true, nil},
		"size":    {`{"wake":true,"context":"` + strings.Repeat("x", MaxGateBytes) + `"}`, true, nil},
		"badre":   {`{"monitor":{"url":"u","ignore":["("]}}`, true, nil},
	} {
		g, err := ParseGate(tc.raw)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: err=%v wantErr=%v", name, err, tc.wantErr)
		}
		if err == nil && tc.wake != nil && (g.Wake == nil || *g.Wake != *tc.wake) {
			t.Fatalf("%s: wake=%v want %v", name, g.Wake, *tc.wake)
		}
	}
}

func TestNormalizeMonitor(t *testing.T) {
	page := `<html><body><div class="side">noise</div><div class="body">
		<p>Price 10</p> <p>at <span class="t">2026-10-30 10:00:01</span></p>
	</div></body></html>`
	other := `<html><body><div class="side">different</div><div class="body">
		<p>Price 10</p><p>at <span class="t">2026-10-30 10:00:59</span></p>
	</div></body></html>`
	ignore := []string{`\d{4}-\d\d-\d\d \d\d:\d\d:\d\d`}
	a, err := normalizeMonitor(page, "div.body", ignore)
	if err != nil {
		t.Fatal(err)
	}
	b, err := normalizeMonitor(other, "div.body", ignore)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("normalized text differs:\n%q\n%q", a, b)
	}
	if strings.Contains(a, "noise") || !strings.Contains(a, "Price 10") {
		t.Fatalf("selector ignored: %q", a)
	}
	c, err := normalizeMonitor(page, "div.body", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c == a {
		t.Fatal("timestamp was not ignored")
	}
}

// TestCronGateNormalized is the COST-13 acceptance: a page whose timestamp
// moves every tick but whose content does not wakes the executor once, then
// stays quiet for ten ticks.
func TestCronGateNormalized(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)}
	job := Job{
		ID: "job1", Name: "watch", Schedule: "*/1 * * * *", Enabled: true,
		Cwd: t.TempDir(), CreatedAt: clock.now.Add(-time.Minute),
		Gate: `{"monitor":{"url":"https://example.test/p","selector":".body","ignore":["\\d{2}:\\d{2}:\\d{2}"]}}`,
	}
	store := newMemStore(job)
	exec := &countedExecutor{}
	hashes := hashStore{}
	ticks := 0
	gates := &Gates{
		Store: hashes,
		Fetch: func(context.Context, string) (string, error) {
			ticks++
			return fmt.Sprintf(`<div class="body">unchanged <span>10:00:%02d</span></div>`, ticks), nil
		},
	}
	s := &Scheduler{Store: store, Executor: exec, Gates: gates, Clock: clock, Owner: "test"}
	for range 11 {
		if err := s.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		clock.now = clock.now.Add(time.Minute)
	}
	if got := exec.count(); got != 1 {
		t.Fatalf("executor calls = %d, want 1 (first wake only)", got)
	}
	gated := 0
	for _, r := range store.rows {
		if r.Outcome == OutcomeGated {
			gated++
		}
	}
	if gated != 10 {
		t.Fatalf("gated rows = %d, want 10", gated)
	}
}

func newTestRunner(t *testing.T) *exec.Runner {
	t.Helper()
	r := exec.New(t.Context())
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Errorf("close runner: %v", err)
		}
	})
	return r
}

func TestCronGateScript(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
	job := Job{ID: "j", Cwd: t.TempDir(), Gate: `{"script":["/bin/sh","-c","exit 1"]}`}
	g, err := ParseGate(job.Gate)
	if err != nil {
		t.Fatal(err)
	}
	runner := newTestRunner(t)
	res, err := (&Gates{Runner: runner}).Check(t.Context(), &job, g)
	if err != nil {
		t.Fatal(err)
	}
	if res.Wake {
		t.Fatal("exit 1 must skip")
	}
	job.Gate = `{"script":["/bin/sh","-c","exit 0"],"context":"today"}`
	g, _ = ParseGate(job.Gate)
	res, err = (&Gates{Runner: runner}).Check(t.Context(), &job, g)
	if err != nil || !res.Wake || res.Context != "today" {
		t.Fatalf("exit 0 = %+v, %v", res, err)
	}
}

func TestCronGateMissingRunnerFailsClosed(t *testing.T) {
	now := time.Now()
	job := Job{ID: "j", Schedule: "*/1 * * * *", Cwd: t.TempDir(), Gate: `{"wake":true}`, CreatedAt: now, Enabled: true}
	store := newMemStore(job)
	clock := &fakeClock{now: now}
	s := &Scheduler{Store: store, Executor: &countedExecutor{}, Clock: clock}
	// A verdict gate needs no runner and runs.
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	job.Gate = `{"monitor":{"url":"https://example.test"}}`
	store.jobs = []Job{job}
	clock.now = clock.now.Add(time.Minute)
	err := s.Tick(t.Context())
	if err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("missing monitor seams = %v", err)
	}
	if errors.Is(err, ErrGateInvalid) {
		t.Fatalf("unexpected parse error: %v", err)
	}
}
