package cron

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
)

func TestParseGate(t *testing.T) {
	wake := false
	for name, tc := range map[string]struct {
		raw     string
		wantErr bool
	}{
		"empty":   {"", false},
		"verdict": {`{"wake":false,"context":"x"}`, false},
		"script":  {`{"script":["true"]}`, false},
		"two":     {`{"wake":true,"script":["true"]}`, true},
		"size":    {`{"wake":true,"context":"` + strings.Repeat("x", MaxGateBytes) + `"}`, true},
	} {
		g, err := ParseGate(tc.raw)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%s: err=%v wantErr=%v", name, err, tc.wantErr)
		}
		if err == nil && tc.raw == `{"wake":false,"context":"x"}` && (g.Wake == nil || *g.Wake != wake) {
			t.Fatalf("%s: wake=%v", name, g.Wake)
		}
	}
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
	g, err = ParseGate(job.Gate)
	if err != nil {
		t.Fatal(err)
	}
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
	if err := s.Tick(t.Context()); err != nil {
		t.Fatal(err)
	}
	job.Gate = `{"script":["/bin/sh","-c","exit 0"]}`
	store.jobs = []Job{job}
	clock.now = clock.now.Add(time.Minute)
	err := s.Tick(t.Context())
	if err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("missing runner = %v", err)
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
