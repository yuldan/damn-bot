// Package delegate implements delegate_task (SPEC §9.3, TOOL-026): it runs
// subagents with their own prompt, role, iteration limit and budget slice,
// folds the child usage back into the parent ledger, enforces a depth and a
// concurrency bound, and owns the per-path leases and optional git worktrees
// of the children (GAP-TOOL-05).
//
// A subagent is executed through the Agent seam (E-09): the composition root
// supplies the loop-backed implementation, so this package never imports the
// provider or the turn loop and every invariant below is testable without a
// network. The package owns no mutable package-level state (E-10): depth,
// budget, leases and slots live in the Service value handed to one session.
package delegate

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/tools"
)

// Limits and defaults of SPEC §9.3 / TOOL-026. MaxDepth is the reviewed
// default of this implementation (SPEC §9.3 originally pinned 1; F5-12
// raises it to 3 with the same per-level budget and lease rules).
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

// Errors returned by Service.Run; task-local failures are Reports instead.
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
	StatusDepthExceeded  = "depth_exceeded"
	StatusConcurrency    = "concurrency_limited"
	StatusWorktreeFailed = "worktree_failed"
	StatusRateLimited    = "rate_limited"
	StatusLedgerFailed   = "ledger_failed"
)

// Task is one subagent request (SPEC §9.3 tasks[]).
type Task struct {
	Goal    string
	Context string
	Role    string
	Files   []string
	// Isolation is "" or "none" for the parent workspace, "worktree" for a
	// private git worktree under the state directory.
	Isolation     string
	MaxIterations int
	BudgetUSD     float64
}

// Request is what the Agent receives. View is the child's own explicit tool
// and capability set (GAP-REL-18); Approvals and Secrets are never carried
// over from the parent (SPEC §9.3), so a child has to ask its own approver.
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

// CostUSD returns the micro-USD cost as USD.
func (u Usage) CostUSD() float64 { return float64(u.CostMicros) / 1e6 }

// AgentResult is one finished subagent run.
type AgentResult struct {
	Answer       string
	StopReason   string
	FilesChanged []string
	Verification string
	Usage        Usage
}

// Agent runs one subagent. Implementations must respect ctx cancellation and
// return the usage they already consumed even on error.
type Agent interface {
	Run(ctx context.Context, req *Request) (AgentResult, error)
}

// AgentFactory builds the Agent for one Request (normally a loop bound to the
// child session and worktree).
type AgentFactory func(ctx context.Context, req *Request) (Agent, error)

// Rollup is one child usage row to add to the parent ledger with
// purpose=delegate (SPEC §9.3, §11.2).
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

// BudgetSource reports the parent's remaining budget before this batch
// (nil = no external cap). The Service tracks its own spent and reserved
// amounts, so the source must not include this batch's delegate rows.
type BudgetSource interface {
	RemainingUSD(ctx context.Context, parentSessionID string) (float64, error)
}

// Notifier receives the rate-limit notice for expensive delegation calls.
// It is optional; failures are ignored because a notice never blocks the
// policy that produced it (TOOL-05/TOOL-08).
type Notifier interface {
	Notice(ctx context.Context, sessionID, kind, message string) error
}

// Config is the fixed policy of one Service. Zero values take the SPEC
// defaults; a value outside its reviewed range is ErrInvalidConfig.
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

// Report is one task's result for the parent model.
type Report struct {
	SessionID    string
	Goal         string
	Status       string
	StopReason   string
	Answer       string
	FilesChanged []string
	Verification string
	Worktree     string
	Owner        string // lease owner on StatusLeaseConflict
	Usage        Usage
	Error        string
}

// Batch is the whole delegate_task result.
type Batch struct {
	Reports   []Report
	Usage     Usage
	Allocated float64
	Spent     float64
	Depth     int
}

// Service owns the delegation state of one process. It is safe for
// concurrent use.
type Service struct {
	cfg      Config
	agents   AgentFactory
	ledger   Ledger
	budget   BudgetSource
	notices  Notifier
	leases   *Leases
	trees    *Worktrees
	calls    *limiter
	notice   *limiter
	slots    chan struct{}
	mu       sync.Mutex
	reserved map[string]float64
	spent    map[string]float64
}

// New validates the required wiring. AgentFactory and Ledger are required: a
// delegate without them would either not run a subagent or lose its usage.
func New(cfg *Config, agents AgentFactory, ledger Ledger, budget BudgetSource, notices Notifier) (*Service, error) {
	c := Config{}
	if cfg != nil {
		c = *cfg
	}
	if err := c.normalize(); err != nil {
		return nil, err
	}
	if agents == nil {
		return nil, fmt.Errorf("%w: agent factory required", ErrInvalidConfig)
	}
	if ledger == nil {
		return nil, fmt.Errorf("%w: ledger required", ErrInvalidConfig)
	}
	return &Service{
		cfg: c, agents: agents, ledger: ledger, budget: budget, notices: notices,
		leases: NewLeases(), trees: NewWorktrees(c.StateDir, c.WorktreeTimeout),
		calls: &limiter{period: c.MinCallPeriod}, notice: &limiter{period: DefaultMinCallPeriod},
		slots:    make(chan struct{}, c.MaxParallel),
		reserved: map[string]float64{}, spent: map[string]float64{},
	}, nil
}

// Leases exposes the per-path lease table (the child file tools receive a
// Guard bound to their own owner).
func (s *Service) Leases() *Leases { return s.leases }

// Worktrees exposes the temporary worktree manager.
func (s *Service) Worktrees() *Worktrees { return s.trees }

// Run executes one delegate_task call. The session view must be explicit and
// grant CapDelegate; depth and the call rate are checked before any task runs
// (GAP-REL-18, expensive-tool coordination).
func (s *Service) Run(ctx context.Context, env tools.Env, tasks []Task, maxParallel int) (Batch, error) {
	view := env.Session
	if err := s.preflight(ctx, view, tasks); err != nil {
		return Batch{}, err
	}
	for i := range tasks {
		if err := validateTask(&tasks[i]); err != nil {
			return Batch{}, fmt.Errorf("%w: task %d: %w", ErrInvalidConfig, i+1, err)
		}
	}
	if maxParallel <= 0 || maxParallel > s.cfg.MaxParallel {
		maxParallel = s.cfg.MaxParallel
	}
	remaining, err := s.remaining(ctx, view.SessionID())
	if err != nil {
		return Batch{}, err
	}
	slicesUSD := s.allocate(remaining, tasks)
	order := s.leaseTasks(view, tasks)
	batch := Batch{Depth: view.Depth()}
	for _, v := range slicesUSD {
		batch.Allocated += v
	}
	s.reserve(view.SessionID(), batch.Allocated)

	reports := s.spawn(ctx, env, view, tasks, slicesUSD, order, maxParallel, newID())
	for i := range reports {
		batch.Usage.Add(reports[i].Usage)
		batch.Spent += reports[i].Usage.CostUSD()
	}
	batch.Reports = reports
	s.release(view.SessionID(), batch.Allocated, batch.Spent)
	return batch, nil
}

// preflight enforces the session, depth, rate and task-count limits before
// any budget is reserved or a subagent starts.
func (s *Service) preflight(ctx context.Context, view tools.SessionView, tasks []Task) error {
	if !view.Valid() {
		return ErrSession
	}
	if !view.Can(tools.CapDelegate) {
		return fmt.Errorf("%w: capability delegate is not granted", ErrSession)
	}
	if view.Depth() >= s.cfg.MaxDepth {
		return fmt.Errorf("%w: depth %d of %d", ErrDepth, view.Depth(), s.cfg.MaxDepth)
	}
	if ok, wait := s.calls.take(view.SessionID(), s.cfg.Clock()); !ok {
		s.notifyRateLimited(ctx, view.SessionID(), wait)
		return fmt.Errorf("%w: retry in %s", ErrRateLimited, wait.Round(time.Millisecond))
	}
	if len(tasks) == 0 {
		return fmt.Errorf("%w: no tasks", ErrInvalidConfig)
	}
	if len(tasks) > s.cfg.MaxTasks {
		return fmt.Errorf("%w: %d tasks, limit %d", ErrInvalidConfig, len(tasks), s.cfg.MaxTasks)
	}
	return nil
}

// spawn runs the tasks with a local concurrency bound and collects their
// reports in task order.
func (s *Service) spawn(ctx context.Context, env tools.Env, view tools.SessionView, tasks []Task, slicesUSD []float64, order map[int]string, maxParallel int, batchID string) []Report {
	reports := make([]Report, len(tasks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxParallel)
	for i := range tasks {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				reports[i] = Report{Goal: tasks[i].Goal, Status: StatusCancelled, StopReason: "cancelled", Error: ctx.Err().Error()}
				return
			}
			if owner, ok := order[i]; ok {
				reports[i] = Report{
					Goal: tasks[i].Goal, Status: StatusLeaseConflict, StopReason: "lease_conflict", Owner: owner,
					Error: fmt.Sprintf("files are leased by delegate %s", owner),
				}
				return
			}
			reports[i] = s.runOne(ctx, env, view, &tasks[i], slicesUSD[i], i, batchID)
		}(i)
	}
	wg.Wait()
	return reports
}

func validateTask(t *Task) error {
	t.Goal = strings.TrimSpace(t.Goal)
	t.Context = strings.TrimSpace(t.Context)
	t.Role = strings.TrimSpace(t.Role)
	if t.Goal == "" || len([]rune(t.Goal)) > MaxGoalChars {
		return fmt.Errorf("goal must be 1..%d chars", MaxGoalChars)
	}
	if len([]rune(t.Context)) > MaxContextChars {
		return fmt.Errorf("context exceeds %d chars", MaxContextChars)
	}
	if len([]rune(t.Role)) > MaxRoleChars {
		return fmt.Errorf("role exceeds %d chars", MaxRoleChars)
	}
	if len(t.Files) > MaxFilesPerTask {
		return fmt.Errorf("files has more than %d entries", MaxFilesPerTask)
	}
	if t.MaxIterations == 0 {
		t.MaxIterations = DefaultIterations
	}
	if t.MaxIterations < 1 || t.MaxIterations > DefaultIterations {
		return fmt.Errorf("max_iterations must be 1..%d", DefaultIterations)
	}
	if t.BudgetUSD < 0 || math.IsNaN(t.BudgetUSD) || t.BudgetUSD > MaxTaskBudgetUSD {
		return fmt.Errorf("budget_usd must be 0..%d", MaxTaskBudgetUSD)
	}
	switch t.Isolation {
	case "", "none":
		t.Isolation = "none"
	case "worktree":
	default:
		return fmt.Errorf("isolation must be none or worktree")
	}
	return nil
}

// allocate turns the remaining budget into explicit task slices whose sum is
// ≤ remaining (SPEC §9.3). A requested slice is capped; an unrequested one
// gets PerTaskShare of what is still available. The scaling is deterministic
// in task order, so a replay reserves the same slices.
func (s *Service) allocate(remaining float64, tasks []Task) []float64 {
	out := make([]float64, len(tasks))
	if remaining <= 0 {
		return out
	}
	if math.IsInf(remaining, 1) {
		for i, t := range tasks {
			out[i] = t.BudgetUSD
		}
		return out
	}
	left := remaining
	for i, t := range tasks {
		if t.BudgetUSD > 0 {
			out[i] = min(t.BudgetUSD, left)
		} else {
			out[i] = math.Min(left, remaining*s.cfg.PerTaskShare)
		}
		if out[i] < 0 || math.IsNaN(out[i]) {
			out[i] = 0
		}
		left -= out[i]
	}
	return out
}

func (s *Service) remaining(ctx context.Context, parent string) (float64, error) {
	total := math.Inf(1)
	if s.budget != nil {
		got, err := s.budget.RemainingUSD(ctx, parent)
		if err != nil {
			return 0, fmt.Errorf("delegate: budget: %w", err)
		}
		if got < 0 || math.IsNaN(got) {
			return 0, fmt.Errorf("delegate: budget: invalid remaining %v", got)
		}
		total = got
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	left := total - s.spent[parent] - s.reserved[parent]
	if math.IsInf(left, 1) {
		return math.Inf(1), nil
	}
	return math.Max(0, left), nil
}

// leaseTasks acquires every task's files in task order. A failed acquisition
// leaves the task out of order; the returned map holds its current owner.
func (s *Service) leaseTasks(view tools.SessionView, tasks []Task) map[int]string {
	owners := make(map[int]string)
	for i := range tasks {
		id := childID(view.SessionID(), i)
		if err := s.leases.Acquire(id, tasks[i].Files); err != nil {
			if ce, ok := errors.AsType[*ConflictError](err); ok {
				owners[i] = ce.Owner
				continue
			}
			owners[i] = "unknown"
		}
	}
	return owners
}

// reserve holds a batch's slices so two concurrent calls cannot both spend
// the same remainder.
func (s *Service) reserve(parent string, amount float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reserved[parent] += amount
}

// release gives back the reservation and folds spent into the parent total.
func (s *Service) release(parent string, reserved, spent float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reserved[parent] = math.Max(0, s.reserved[parent]-reserved)
	s.spent[parent] += spent
}

func (s *Service) runOne(ctx context.Context, env tools.Env, parent tools.SessionView, task *Task, budget float64, idx int, batchID string) (rep Report) {
	rep = Report{SessionID: childID(parent.SessionID(), idx), Goal: task.Goal}
	defer s.leases.Release(rep.SessionID)
	if budget <= 0 && s.budget != nil {
		rep.Status, rep.StopReason = StatusBudgetExceeded, "budget_exhausted"
		rep.Error = "the parent budget has no slice left"
		return rep
	}
	cwd, worktree, cleanup, werr := s.taskWorktree(ctx, parent, task, idx, batchID, &rep)
	if werr != nil {
		rep.Status, rep.StopReason = StatusWorktreeFailed, "worktree_failed"
		rep.Error = werr.Error()
		return rep
	}
	if cleanup != nil {
		defer cleanup()
	}
	toolNames := parent.Tools()
	if parent.Depth()+1 >= s.cfg.MaxDepth {
		toolNames = removeTool(toolNames, "delegate_task")
	}
	view, err := parent.ChildIn(rep.SessionID, cwd, toolNames, parent.Capabilities())
	if err != nil {
		rep.Status, rep.StopReason = StatusFailed, "session_view"
		rep.Error = err.Error()
		return rep
	}
	req := Request{
		SessionID: rep.SessionID, ParentSessionID: parent.SessionID(), TurnID: env.TurnID,
		Goal: task.Goal, Context: task.Context, Role: task.Role, Files: slices.Clone(task.Files),
		Cwd: cwd, Worktree: worktree, MaxIterations: task.MaxIterations,
		BudgetUSD: budget, Depth: view.Depth(), View: view,
	}
	if cerr := ctx.Err(); cerr != nil {
		rep.Status, rep.StopReason = StatusCancelled, "cancelled"
		rep.Error = cerr.Error()
		return rep
	}
	agent, err := s.agents(ctx, &req)
	if err != nil {
		rep.Status, rep.StopReason = StatusFailed, "agent_unavailable"
		rep.Error = err.Error()
		return rep
	}
	res, runErr := agent.Run(ctx, &req)
	rep.Usage = res.Usage
	rep.Answer = clip(res.Answer, AnswerChars)
	rep.StopReason = res.StopReason
	rep.FilesChanged = append(rep.FilesChanged, res.FilesChanged...)
	rep.Verification = res.Verification
	switch {
	case runErr != nil && errors.Is(runErr, context.Canceled):
		rep.Status, rep.StopReason = StatusCancelled, "cancelled"
		rep.Error = runErr.Error()
	case runErr != nil:
		rep.Status, rep.StopReason = StatusFailed, "agent_error"
		rep.Error = runErr.Error()
	case rep.StopReason == "":
		rep.Status, rep.StopReason = StatusCompleted, "end_turn"
	default:
		rep.Status = StatusCompleted
	}
	if rerr := s.ledger.RecordDelegate(context.WithoutCancel(ctx), &Rollup{
		ParentSessionID: parent.SessionID(), TurnID: env.TurnID, ChildSessionID: rep.SessionID,
		Depth: view.Depth(), Usage: res.Usage,
	}); rerr != nil {
		rep.Status, rep.StopReason = StatusLedgerFailed, "ledger_error"
		rep.Error = joinError(rep.Error, rerr.Error())
	}
	return rep
}

// taskWorktree opens the task's optional worktree and returns its cleanup.
// Cleanup runs even on cancellation: the parent's cancel must not leave a
// worktree behind, and a dirty tree is retained and reported.
func (s *Service) taskWorktree(ctx context.Context, parent tools.SessionView, task *Task, idx int, batchID string, rep *Report) (cwd, path string, cleanup func(), err error) {
	cwd = parent.Cwd()
	if task.Isolation != "worktree" {
		return cwd, "", nil, nil
	}
	wt, err := s.trees.Create(ctx, parent.Cwd(), batchID+"-"+fmt.Sprint(idx+1))
	if err != nil {
		return "", "", nil, err
	}
	rep.Worktree = wt.Path
	cleanup = func() {
		cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.cfg.WorktreeTimeout)
		defer cancel()
		clean, changed, cerr := wt.Finish(cctx)
		rep.FilesChanged = changed
		if clean {
			rep.Worktree = ""
		}
		if cerr != nil {
			rep.Error = joinError(rep.Error, "worktree cleanup: "+cerr.Error())
		}
	}
	return wt.Path, wt.Path, cleanup, nil
}

func (s *Service) notifyRateLimited(ctx context.Context, sessionID string, wait time.Duration) {
	if s.notices == nil {
		return
	}
	if ok, _ := s.notice.take(sessionID, s.cfg.Clock()); !ok {
		return
	}
	if err := s.notices.Notice(context.WithoutCancel(ctx), sessionID, "delegate_rate_limited",
		fmt.Sprintf("delegate_task is rate limited; retry in %s", wait.Round(time.Millisecond))); err != nil {
		return
	}
}

func childID(parent string, i int) string { return fmt.Sprintf("%s-d%d", parent, i+1) }

func removeTool(names []string, drop string) []string {
	out := names[:0]
	for _, n := range names {
		if n != drop {
			out = append(out, n)
		}
	}
	return out
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func joinError(old, add string) string {
	if old == "" {
		return add
	}
	return old + "; " + add
}

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000000000000000"
	}
	return hex.EncodeToString(b[:])
}
