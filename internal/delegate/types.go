// Package delegate implements delegate_task (SPEC §9.3, TOOL-026): subagents
// with their own prompt/role/iteration/budget, depth and concurrency bounds,
// per-path leases and optional worktrees (GAP-TOOL-05). Execution goes through
// the Agent seam (E-09); state lives in a Service value (E-10).

package delegate

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/tools"
)

// Limits and defaults of SPEC §9.3 / TOOL-026 (depth 1 -> 3, ADR-273).
const (
	DefaultMaxDepth      = 3
	DefaultMaxParallel   = 3
	DefaultMaxTasks      = 10
	DefaultIterations    = 40
	DefaultPerTaskShare  = 0.25
	DefaultMinCallPeriod = 15 * time.Second
	MaxTaskBudgetUSD     = 1_000_000
	MaxFilesPerTask      = 64
	MaxGoalChars         = 4000
	MaxContextChars      = 8000
	MaxRoleChars         = 64
	AnswerChars          = 2000
)

// Errors of Service.Run; task-local failures are Reports.
var (
	ErrInvalidConfig = errors.New("delegate: invalid configuration")
	ErrSession       = errors.New("delegate: session view required")
	ErrDepth         = errors.New("delegate: maximum delegation depth reached")
	ErrRateLimited   = errors.New("delegate: delegation rate limited")
)

// Stop reasons reported for one task.
const (
	StatusCompleted      = "completed"
	StatusFailed         = "failed"
	StatusCancelled      = "cancelled"
	StatusBudgetExceeded = "budget_exhausted"
	StatusLeaseConflict  = "lease_conflict"
	StatusWorktreeFailed = "worktree_failed"
	StatusLedgerFailed   = "ledger_failed"
)

// Task is one subagent request (SPEC §9.3 tasks[]).
type Task struct {
	Goal    string
	Context string
	Role    string
	Files   []string
	// Isolation is ""/"none" for the parent workspace; "worktree" is private.
	Isolation     string
	MaxIterations int
	BudgetUSD     float64
}

// Request is what the Agent receives; View is the child's explicit tool set.
type Request struct {
	SessionID       string
	ParentSessionID string
	TurnID          string
	Goal            string
	Context         string
	Role            string
	Files           []string
	Cwd             string
	Worktree        string
	MaxIterations   int
	BudgetUSD       float64
	Depth           int
	View            tools.SessionView
}

// Usage is the child usage folded into the parent ledger.
type Usage struct {
	Input, CachedInput, CacheCreation, Output int64
	Calls                                     int64
	CostMicros                                int64
}

// Add accumulates u.
func (u *Usage) Add(o Usage) {
	u.Input += o.Input
	u.CachedInput += o.CachedInput
	u.CacheCreation += o.CacheCreation
	u.Output += o.Output
	u.Calls += o.Calls
	u.CostMicros += o.CostMicros
}

// AgentResult is one finished subagent run.
type AgentResult struct {
	Answer       string
	StopReason   string
	FilesChanged []string
	Verification string
	Usage        Usage
}

// Agent runs one subagent and returns the usage already consumed on error.
type Agent interface {
	Run(ctx context.Context, req *Request) (AgentResult, error)
}

// AgentFactory builds the Agent for one Request.
type AgentFactory func(ctx context.Context, req *Request) (Agent, error)

// Rollup is one child usage row for the parent ledger (purpose=delegate).
type Rollup struct {
	ParentSessionID string
	TurnID          string
	ChildSessionID  string
	Depth           int
	Usage           Usage
}

// Ledger records the rollup. The composition root inserts it in the same
// transaction that makes the child usage durable.
type Ledger interface {
	RecordDelegate(ctx context.Context, r *Rollup) error
}

// BudgetSource reports the parent's remaining budget before this batch; nil
// means no external cap. It must exclude this batch's delegate rows.
type BudgetSource interface {
	RemainingUSD(ctx context.Context, parentSessionID string) (float64, error)
}

// Notifier receives the rate-limit notice; failures never block the policy.
type Notifier interface {
	Notice(ctx context.Context, sessionID, kind, message string) error
}

// Config is the fixed policy; zero values take the SPEC defaults.
type Config struct {
	MaxDepth        int
	MaxParallel     int
	MaxTasks        int
	DefaultIter     int
	PerTaskShare    float64
	MinCallPeriod   time.Duration
	StateDir        string // worktree root parent: <state_dir>/worktrees
	Clock           func() time.Time
	WorktreeTimeout time.Duration
}

func (c *Config) normalize() error {
	if c.MaxDepth == 0 {
		c.MaxDepth = DefaultMaxDepth
	}
	if c.MaxParallel == 0 {
		c.MaxParallel = DefaultMaxParallel
	}
	if c.MaxTasks == 0 {
		c.MaxTasks = DefaultMaxTasks
	}
	if c.DefaultIter == 0 {
		c.DefaultIter = DefaultIterations
	}
	if c.PerTaskShare == 0 {
		c.PerTaskShare = DefaultPerTaskShare
	}
	if c.MinCallPeriod == 0 {
		c.MinCallPeriod = DefaultMinCallPeriod
	}
	if c.Clock == nil {
		c.Clock = time.Now
	}
	if c.WorktreeTimeout == 0 {
		c.WorktreeTimeout = 30 * time.Second
	}
	return c.validate()
}

// validate checks the reviewed ranges after defaults are applied.
func (c *Config) validate() error {
	if c.MaxDepth < 1 || c.MaxDepth > 16 {
		return fmt.Errorf("%w: max_depth must be 1..16", ErrInvalidConfig)
	}
	if c.MaxParallel < 1 || c.MaxParallel > 16 {
		return fmt.Errorf("%w: max_parallel must be 1..16", ErrInvalidConfig)
	}
	if c.MaxTasks < 1 || c.MaxTasks > 32 {
		return fmt.Errorf("%w: max_tasks must be 1..32", ErrInvalidConfig)
	}
	if c.DefaultIter < 1 || c.DefaultIter > 40 {
		return fmt.Errorf("%w: default_iterations must be 1..40", ErrInvalidConfig)
	}
	if c.PerTaskShare <= 0 || c.PerTaskShare > 1 {
		return fmt.Errorf("%w: per_task_share must be in (0,1]", ErrInvalidConfig)
	}
	if c.MinCallPeriod < 0 {
		return fmt.Errorf("%w: min_call_period must not be negative", ErrInvalidConfig)
	}
	return nil
}
