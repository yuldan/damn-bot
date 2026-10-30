package cron

import (
	"context"
	"errors"
	"testing"
	"time"
)

type adminStore struct {
	*memStore
	added []Job
}

func newAdminStore(jobs ...Job) *adminStore {
	return &adminStore{memStore: newMemStore(jobs...)}
}

func (s *adminStore) Create(_ context.Context, j *Job) (Job, bool, error) {
	s.added = append(s.added, *j)
	return *j, true, nil
}

func (s *adminStore) Update(context.Context, *Job) error { return nil }

func (s *adminStore) SetEnabled(context.Context, string, bool, string) error { return nil }

func (s *adminStore) Remove(context.Context, string) error { return nil }

func (s *adminStore) ActiveJobs(_ context.Context, owner string) (int, error) {
	n := 0
	for i := range s.jobs {
		j := &s.jobs[i]
		if j.Enabled && (j.Owner == owner || j.Owner == "" && owner == "local") {
			n++
		}
	}
	return n, nil
}

func TestCronMinIntervalGuard(t *testing.T) {
	s := newAdminStore()
	a := &Admin{Store: s, Owner: "local", Limits: Limits{MinInterval: 15 * time.Minute, MaxJobs: 20}}
	if _, _, err := a.Create(t.Context(), &Job{Name: "n", Schedule: "* * * * *", Prompt: "p"}); !errors.Is(err, ErrMinInterval) {
		t.Fatalf("agent 1m = %v, want ErrMinInterval", err)
	}
	// A user-driven CLI may lower the floor to one minute.
	a.Limits.MinInterval = time.Minute
	if _, _, err := a.Create(t.Context(), &Job{Name: "n", Schedule: "* * * * *", Prompt: "p"}); err != nil {
		t.Fatalf("user 1m = %v", err)
	}
	a.Limits.MinInterval = 15 * time.Minute
	if _, _, err := a.Create(t.Context(), &Job{Name: "ok", Schedule: "*/15 * * * *", Prompt: "p"}); err != nil {
		t.Fatalf("15m = %v", err)
	}
	if len(s.added) != 2 {
		t.Fatalf("created %d jobs, want 2", len(s.added))
	}
	if s.added[1].Owner != "local" {
		t.Fatalf("owner = %q, want local", s.added[1].Owner)
	}
}

func TestCronJobQuota(t *testing.T) {
	jobs := make([]Job, 20)
	for i := range jobs {
		jobs[i] = Job{ID: string(rune('a' + i)), Name: "j", Owner: "local", Enabled: true}
	}
	s := newAdminStore(jobs...)
	a := &Admin{Store: s, Owner: "local", Limits: Limits{MinInterval: time.Minute, MaxJobs: 20}}
	_, _, err := a.Create(t.Context(), &Job{Name: "21", Schedule: "0 * * * *", Prompt: "p"})
	if !errors.Is(err, ErrJobQuota) {
		t.Fatalf("21st job = %v, want ErrJobQuota", err)
	}
	// Another owner has its own quota.
	a.Owner = "other"
	if _, _, err := a.Create(t.Context(), &Job{Name: "o", Schedule: "0 * * * *", Prompt: "p"}); err != nil {
		t.Fatalf("other owner = %v", err)
	}
}
