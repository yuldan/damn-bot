package delegate

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/tools"
)

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

// Service owns the delegation state of one process; safe for concurrent use.
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

// New validates the required wiring: AgentFactory and Ledger are mandatory.
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

// Leases exposes the lease table for owner-bound guards.
func (s *Service) Leases() *Leases { return s.leases }

// Worktrees exposes the temporary worktree manager.
func (s *Service) Worktrees() *Worktrees { return s.trees }

// Run executes one delegate_task call; the explicit view must grant
// CapDelegate, and depth/rate are checked before any task runs.
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
		batch.Spent += float64(reports[i].Usage.CostMicros) / 1e6
	}
	batch.Reports = reports
	s.release(view.SessionID(), batch.Allocated, batch.Spent)
	return batch, nil
}

// preflight enforces session, depth, rate and task-count limits.
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

// spawn runs the tasks with a local concurrency bound in task order.
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

// allocate turns the remainder into slices whose sum is ≤ remaining, in task
// order so a replay reserves the same slices (SPEC §9.3).
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

// leaseTasks acquires each task's files; the map holds a failed task's owner.
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

// reserve holds a batch's slices against concurrent batches.
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
	atLimit := parent.Depth()+1 >= s.cfg.MaxDepth
	toolNames := slices.DeleteFunc(parent.Tools(), func(n string) bool { return atLimit && n == "delegate_task" })
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

// taskWorktree opens the optional worktree; cleanup runs even on cancellation
// and a dirty tree is retained and reported.
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

func newID() string { return strings.ToLower(rand.Text())[:16] }
