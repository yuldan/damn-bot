package tools

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/cron"
)

// cronFake is an in-memory cron.AdminStore; mutating calls are recorded so a
// denied approval proves zero changes.
type cronFake struct {
	mu    sync.Mutex
	jobs  []cron.Job
	calls []string
}

func (f *cronFake) note(name string) {
	f.mu.Lock()
	f.calls = append(f.calls, name)
	f.mu.Unlock()
}

func (f *cronFake) mutations() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *cronFake) Jobs(context.Context) ([]cron.Job, error) {
	f.note("list")
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]cron.Job(nil), f.jobs...), nil
}

func (f *cronFake) Create(_ context.Context, j *cron.Job) (cron.Job, bool, error) {
	f.note("create")
	j.ID = "job-1"
	f.mu.Lock()
	f.jobs = append(f.jobs, *j)
	f.mu.Unlock()
	return *j, true, nil
}

func (f *cronFake) Update(context.Context, *cron.Job) error {
	f.note("update")
	return nil
}

func (f *cronFake) SetEnabled(context.Context, string, bool, string) error {
	f.note("enabled")
	return nil
}
func (f *cronFake) Remove(context.Context, string) error { f.note("remove"); return nil }
func (f *cronFake) State(context.Context, string) (cron.State, error) {
	return cron.State{}, nil
}

func (f *cronFake) Active(context.Context, string) (cron.Active, error) {
	return cron.Active{}, nil
}

func (f *cronFake) Claim(context.Context, string, time.Time, time.Time, string) (bool, error) {
	f.note("claim")
	return true, nil
}

func (f *cronFake) TakeQueued(context.Context, string, time.Time) (time.Time, bool, error) {
	return time.Time{}, false, nil
}
func (f *cronFake) Record(context.Context, string, time.Time, *cron.Run) error { return nil }
func (f *cronFake) Finish(context.Context, string, time.Time, *cron.Run) error {
	f.note("finish")
	return nil
}

func cronDispatcher(t *testing.T, mode Mode, appr Approver, admin *cron.Admin) *Dispatcher {
	t.Helper()
	d, err := NewDispatcher(&Config{
		Registry: mustReg(t, NewCronJob(admin)), Journal: &journalRec{}, Spill: newMemSpill(),
		Redactor: testRedactor(), Mode: mode, Approver: appr, WorkspaceTrusted: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

type okExec struct{}

func (okExec) Run(context.Context, *cron.Job) (string, error) { return "ok", nil }

const cronCreateArgs = `{"action":"create","schedule":"*/15 * * * *","prompt":"collect the report","name":"report"}`

// TestCronMutationNeedsApproval is the TOOL-025 gate: no auto mode, no
// session grant; a denied or absent approver leaves the store untouched.
func TestCronMutationNeedsApproval(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mode    Mode
		appr    Approver
		want    Code
		allowed bool
	}{
		{"ask/no approver", ModeAsk, nil, CodeDenied, false},
		{"ask/deny", ModeAsk, &approverRec{answer: ApprovalDeny}, CodeDenied, false},
		{"auto/no approver", ModeAuto, nil, CodeDenied, false},
		{"auto-edit/no approver", ModeAutoEdit, nil, CodeDenied, false},
		{"headless", ModeHeadless, nil, CodeDenied, false},
		{"ask/allow", ModeAsk, &approverRec{answer: ApprovalAllowOnce}, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			store := &cronFake{}
			admin := &cron.Admin{Store: store, Exec: okExec{}}
			d := cronDispatcher(t, c.mode, c.appr, admin)
			out := d.Dispatch(context.Background(), callOf("cronjob_manage", cronCreateArgs))
			if got := codeOfOutcome(out); got != c.want {
				t.Fatalf("code = %q, want %q (%s)", got, c.want, out.Render())
			}
			if allowed := store.mutations() > 0; allowed != c.allowed {
				t.Fatalf("store calls = %v, want mutations=%v", store.calls, c.allowed)
			}
			if rec, ok := c.appr.(*approverRec); ok && len(rec.requests()) > 0 {
				req := rec.requests()[0]
				if req.AllowSession || req.Class != "cron:manage" {
					t.Fatalf("approval request = %+v", req)
				}
			}
		})
	}
}

// TestCronListDoesNotAsk: the read-only action runs without an approver.
func TestCronListDoesNotAsk(t *testing.T) {
	t.Parallel()
	store := &cronFake{jobs: []cron.Job{{ID: "j1", Name: "daily", Schedule: "0 9 * * *", Enabled: true}}}
	admin := &cron.Admin{Store: store}
	d := cronDispatcher(t, ModeHeadless, nil, admin)
	out := d.Dispatch(context.Background(), callOf("cronjob_manage", `{"action":"list"}`))
	if out.Err != nil {
		t.Fatalf("list: %s", out.Render())
	}
	if store.mutations() != 1 {
		t.Fatalf("list called store %d times", store.mutations())
	}
}

// TestCronEffectClasses: only list is read; every mutation is non-idempotent.
func TestCronEffectClasses(t *testing.T) {
	t.Parallel()
	tool := NewCronJob(&cron.Admin{Store: &cronFake{}})
	for _, c := range []struct {
		args string
		want Effect
	}{
		{`{"action":"list"}`, EffectRead},
		{`{"action":"get","job_id":"x"}`, EffectNonIdempotent},
		{`{"action":"create"}`, EffectNonIdempotent},
		{`{"action":"run","job_id":"x"}`, EffectNonIdempotent},
		{`{"action":"bogus"}`, EffectNonIdempotent},
	} {
		if got := tool.Effect(json.RawMessage(c.args)); got != c.want {
			t.Fatalf("%s effect = %s, want %s", c.args, got, c.want)
		}
	}
}
