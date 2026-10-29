package cron

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// AUTO-011 limits: parent-chain depth and active jobs per owner (COST-14).
const (
	DefaultSelfDepth = 5
	DefaultSelfJobs  = 20
)

var (
	// ErrDepth rejects a child created past the parent-chain depth limit.
	ErrDepth = errors.New("cron: self-schedule depth exceeded")
	// ErrQuota rejects a child when the owner already holds the maximum jobs.
	ErrQuota = errors.New("cron: active job quota reached")
)

// Parent identifies the run that asks to schedule a child; At is its
// scheduled_at, so one run creates at most one child per key.
type Parent struct {
	JobID string
	At    time.Time
	Depth int
}

// Child is the job an agent wants to schedule.
type Child struct {
	Name, Schedule, Prompt, Owner, Cwd, TZ string
}

// Spawn is the persisted child spec; ID and Name are deterministic per
// (parent, scheduled_at) so retries cannot create a second job.
type Spawn struct {
	ID, Name, Owner, Schedule, TZ, Prompt, Cwd string
	Enabled                                    bool
}

// Spawner persists agent-created jobs. Has makes retries idempotent before the
// quota check, ActiveJobs counts enabled jobs of an owner, and Spawn returns
// the existing id when the job already exists.
type Spawner interface {
	Has(ctx context.Context, id string) (bool, error)
	ActiveJobs(ctx context.Context, owner string) (int, error)
	Spawn(ctx context.Context, spec *Spawn) (string, error)
}

// SelfSchedule is the AUTO-011 guard; zero limits take the defaults.
type SelfSchedule struct {
	Spawner  Spawner
	MaxDepth int
	MaxJobs  int
}

// Schedule admits and persists one child job; the same (parent, scheduled_at)
// always maps to the same id, so a retried run returns the original job.
func (s *SelfSchedule) Schedule(ctx context.Context, parent Parent, child *Child) (string, error) {
	if s.Spawner == nil {
		return "", errors.New("cron: nil spawner")
	}
	if parent.JobID == "" || parent.At.IsZero() {
		return "", errors.New("cron: self-schedule needs a parent job and slot")
	}
	depth := s.MaxDepth
	if depth <= 0 {
		depth = DefaultSelfDepth
	}
	if parent.Depth >= depth {
		return "", fmt.Errorf("%w: depth %d >= %d", ErrDepth, parent.Depth, depth)
	}
	id := childID(parent)
	exists, err := s.Spawner.Has(ctx, id)
	if err != nil {
		return "", fmt.Errorf("cron: spawner lookup: %w", err)
	}
	if exists {
		return id, nil
	}
	active, err := s.Spawner.ActiveJobs(ctx, child.Owner)
	if err != nil {
		return "", fmt.Errorf("cron: spawner count: %w", err)
	}
	quota := s.MaxJobs
	if quota <= 0 {
		quota = DefaultSelfJobs
	}
	if active >= quota {
		return "", fmt.Errorf("%w: %d active jobs", ErrQuota, active)
	}
	got, err := s.Spawner.Spawn(ctx, &Spawn{
		ID: id, Name: childName(child.Name, id), Owner: child.Owner,
		Schedule: child.Schedule, TZ: child.TZ, Prompt: child.Prompt,
		Cwd: child.Cwd, Enabled: true,
	})
	if err != nil {
		return "", fmt.Errorf("cron: spawn child: %w", err)
	}
	return got, nil
}

// childID is the idempotency key of (parent, scheduled_at).
func childID(p Parent) string {
	sum := sha256.Sum256([]byte(p.JobID + "\x00" + p.At.UTC().Format(time.RFC3339Nano)))
	return "self-" + hex.EncodeToString(sum[:8])
}

func childName(name, id string) string {
	if name == "" {
		return id
	}
	return name + "-" + id[len("self-"):]
}
