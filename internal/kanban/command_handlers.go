package kanban

import (
	"context"
	"fmt"
	"time"
)

func (iv *Invocation) cmdBoard(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	name := fs.String("name", iv.Board, "board name")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	if sub == "boards" {
		return iv.Store.Boards(ctx)
	}
	return iv.Store.InitBoard(ctx, *name, iv.Actor)
}

func (iv *Invocation) cmdCreate(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	title := fs.String("title", "", "task title (required)")
	body := fs.String("body", "", "task body")
	status := fs.String("status", "", "backlog|ready")
	assignee := fs.String("assignee", "", "worker")
	priority := fs.Int("priority", -1, "0..4")
	due := fs.String("due", "", "RFC3339")
	var labels, links multi
	fs.Var(&labels, "label", "label (repeat)")
	fs.Var(&links, "link", "dependency id (repeat)")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	spec := TaskSpec{Title: *title, Body: *body, Assignee: *assignee, Labels: labels, Links: links}
	if *status != "" {
		spec.Status = Status(*status)
	}
	if *priority >= 0 {
		spec.Priority = priority
	}
	if *due != "" {
		at, err := time.Parse(time.RFC3339, *due)
		if err != nil {
			return nil, fmt.Errorf("%w: --due needs RFC3339", ErrInvalid)
		}
		spec.DueAt = &at
	}
	t, created, err := iv.Store.CreateTask(ctx, iv.Board, &spec, iv.Actor)
	if err != nil {
		return nil, err
	}
	return map[string]any{"task": t, "created": created}, nil
}

func (iv *Invocation) cmdSwarm(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	var profiles, titles multi
	fs.Var(&profiles, "profile", "worker profile (repeat)")
	fs.Var(&titles, "title", "child title (repeat)")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	id, err := iv.taskArg(fs.Args())
	if err != nil {
		return nil, err
	}
	plan := SwarmPlan{Profiles: profiles}
	for _, title := range titles {
		plan.Tasks = append(plan.Tasks, ChildSpec{Title: title})
	}
	return iv.Store.Swarm(ctx, id, plan, iv.Actor)
}

func (iv *Invocation) cmdList(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	status := fs.String("status", "", "filter status")
	assignee := fs.String("assignee", "", "filter assignee")
	limit := fs.Int("limit", 0, "row limit")
	archived := fs.Bool("archived", false, "show archived")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	return iv.Store.Tasks(ctx, iv.Board, Filter{Status: Status(*status), Assignee: *assignee, Limit: *limit, Archived: *archived})
}

func (iv *Invocation) cmdShow(ctx context.Context, sub string, args []string) (any, error) {
	id, err := iv.oneTaskArg(sub, args)
	if err != nil {
		return nil, err
	}
	t, err := iv.Store.Task(ctx, id)
	if err != nil {
		return nil, err
	}
	evs, err := iv.Store.TaskEvents(ctx, id, 0)
	if err != nil {
		return nil, err
	}
	return map[string]any{"task": t, "events": evs}, nil
}

func (iv *Invocation) cmdAssign(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	assignee := fs.String("assignee", "", "worker")
	model := fs.String("model", "", "model ref")
	force := fs.Bool("force", false, "reclaim a live lease")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	id, err := iv.taskArg(fs.Args())
	if err != nil {
		return nil, err
	}
	switch sub {
	case "assign":
		return iv.Store.Assign(ctx, id, *assignee, iv.Actor)
	case "reassign":
		return iv.Store.Reassign(ctx, id, *assignee, iv.Actor)
	case "set-model":
		return iv.Store.SetModel(ctx, id, *model, iv.Actor)
	default:
		return iv.Store.Reclaim(ctx, id, iv.Actor, *force)
	}
}

func (iv *Invocation) cmdDiagnostics(ctx context.Context, sub string, args []string) (any, error) {
	if err := parse(iv.flags(sub), args); err != nil {
		return nil, err
	}
	return iv.Store.Diagnostics(ctx, iv.Board)
}

func (iv *Invocation) cmdLink(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	dep := fs.String("dep", "", "dependency id")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	id, err := iv.taskArg(fs.Args())
	if err != nil {
		return nil, err
	}
	if sub == "link" {
		return iv.Store.Link(ctx, id, *dep, iv.Actor)
	}
	return iv.Store.Unlink(ctx, id, *dep, iv.Actor)
}

func (iv *Invocation) cmdClaim(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	worker := fs.String("worker", iv.Actor, "worker id")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	rest := fs.Args()
	if len(rest) > 0 {
		return iv.Store.ClaimTask(ctx, rest[0], *worker, 0)
	}
	return iv.Store.Claim(ctx, iv.Board, *worker, 0)
}

func (iv *Invocation) cmdComment(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	text := fs.String("text", "", "comment")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	id, err := iv.taskArg(fs.Args())
	if err != nil {
		return nil, err
	}
	return iv.Store.Comment(ctx, id, *text, iv.Actor)
}

func (iv *Invocation) cmdAttach(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	path := fs.String("path", "", "workspace file")
	name := fs.String("name", "", "attachment name")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	id, err := iv.taskArg(fs.Args())
	if err != nil {
		return nil, err
	}
	switch sub {
	case "attach":
		return iv.Store.AttachFile(ctx, id, *path, iv.Actor)
	case "attach-rm":
		return iv.Store.Detach(ctx, id, *name, iv.Actor)
	default:
		return iv.Store.Attachments(ctx, id)
	}
}

func (iv *Invocation) cmdComplete(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	summary := fs.String("summary", "", "handoff summary")
	commit := fs.String("commit", "", "commit")
	runID := fs.String("run", "", "run id")
	cost := fs.Int64("cost-micros", 0, "billed cost")
	var files, verify multi
	fs.Var(&files, "file", "handoff file (repeat)")
	fs.Var(&verify, "verify", "verification (repeat)")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	id, err := iv.taskArg(fs.Args())
	if err != nil {
		return nil, err
	}
	var run *Run
	if *runID != "" {
		run = &Run{ID: *runID, Worker: iv.Actor, Outcome: "ok", CostMicros: *cost, FinishedAt: time.Now().UTC()}
	}
	return iv.Store.Complete(ctx, id, &Handoff{Summary: *summary, Files: files, Commit: *commit, Verify: verify}, run, iv.Actor)
}

func (iv *Invocation) cmdEdit(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	title := fs.String("title", "", "title")
	body := fs.String("body", "", "body")
	priority := fs.Int("priority", -1, "0..4")
	var labels multi
	fs.Var(&labels, "label", "label (repeat)")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	id, err := iv.taskArg(fs.Args())
	if err != nil {
		return nil, err
	}
	spec := TaskSpec{Title: *title, Body: *body, Labels: labels}
	if *priority >= 0 {
		spec.Priority = priority
	}
	return iv.Store.Edit(ctx, id, &spec, iv.Actor)
}

func (iv *Invocation) cmdBlock(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	reason := fs.String("reason", "", "blocking reason")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	id, err := iv.taskArg(fs.Args())
	if err != nil {
		return nil, err
	}
	return iv.Store.Block(ctx, id, *reason, iv.Actor)
}

func (iv *Invocation) cmdSchedule(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	at := fs.String("at", "", "RFC3339")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	id, err := iv.taskArg(fs.Args())
	if err != nil {
		return nil, err
	}
	when, err := time.Parse(time.RFC3339, *at)
	if err != nil {
		return nil, fmt.Errorf("%w: --at needs RFC3339", ErrInvalid)
	}
	return iv.Store.Schedule(ctx, id, when, iv.Actor)
}

func (iv *Invocation) cmdReview(ctx context.Context, sub string, args []string) (any, error) {
	id, err := iv.oneTaskArg(sub, args)
	if err != nil {
		return nil, err
	}
	switch sub {
	case "unblock":
		return iv.Store.Unblock(ctx, id, iv.Actor)
	case "request-review":
		return iv.Store.RequestReview(ctx, id, iv.Actor)
	case "promote":
		return iv.Store.Promote(ctx, id, iv.Actor)
	case "archive":
		return iv.Store.Archive(ctx, id, iv.Actor)
	default:
		return iv.Store.ReopenReview(ctx, id, "reopen", iv.Actor)
	}
}

func (iv *Invocation) cmdRequestChanges(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	reason := fs.String("reason", "", "change request")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	id, err := iv.taskArg(fs.Args())
	if err != nil {
		return nil, err
	}
	return iv.Store.RequestChanges(ctx, id, *reason, iv.Actor)
}

func (iv *Invocation) cmdTail(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	limit := fs.Int("limit", 20, "events")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	return iv.Store.Tail(ctx, iv.Board, *limit)
}

func (iv *Invocation) cmdLog(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	limit := fs.Int("limit", 0, "events")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	if len(fs.Args()) > 0 {
		return iv.Store.TaskEvents(ctx, fs.Args()[0], *limit)
	}
	return iv.Store.Tail(ctx, iv.Board, *limit)
}

func (iv *Invocation) cmdDispatch(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	var workers multi
	fs.Var(&workers, "worker", "worker id (repeat)")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	if len(workers) == 0 {
		workers = []string{iv.Actor}
	}
	d := &Dispatcher{Store: iv.Store, Board: iv.Board, Workers: workers, Runner: iv.Runner}
	n, err := d.DispatchOnce(ctx)
	return map[string]any{"dispatched": n}, err
}

func (iv *Invocation) cmdDaemon(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	var workers multi
	fs.Var(&workers, "worker", "worker id (repeat)")
	once := fs.Bool("once", false, "one tick")
	interval := fs.Duration("interval", 30*time.Second, "tick interval")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	if len(workers) == 0 {
		workers = []string{iv.Actor}
	}
	d := &Dispatcher{Store: iv.Store, Board: iv.Board, Workers: workers, Runner: iv.Runner}
	if *once {
		n, err := d.DispatchOnce(ctx)
		return map[string]any{"dispatched": n}, err
	}
	if iv.Runner == nil {
		return nil, fmt.Errorf("%w: daemon needs a runner", ErrNotConfigured)
	}
	return nil, d.Run(ctx, *interval)
}

func (iv *Invocation) cmdStats(ctx context.Context, sub string, args []string) (any, error) {
	if err := parse(iv.flags(sub), args); err != nil {
		return nil, err
	}
	return iv.Store.Stats(ctx, iv.Board)
}

func (iv *Invocation) cmdNotify(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	target := fs.String("target", "", "platform:chat")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	switch sub {
	case "notify-subscribe":
		return iv.Store.Subscribe(ctx, iv.Board, *target)
	case "notify-unsubscribe":
		if err := iv.Store.Unsubscribe(ctx, iv.Board, *target); err != nil {
			return nil, err
		}
		return map[string]bool{"removed": true}, nil
	default:
		return iv.Store.Notifications(ctx, iv.Board)
	}
}

func (iv *Invocation) cmdRuns(ctx context.Context, sub string, args []string) (any, error) {
	if err := parse(iv.flags(sub), args); err != nil {
		return nil, err
	}
	return iv.Store.Runs(ctx, iv.Board)
}

func (iv *Invocation) cmdHeartbeat(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	worker := fs.String("worker", iv.Actor, "worker id")
	ttl := fs.Duration("ttl", 0, "lease ttl")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	id, err := iv.taskArg(fs.Args())
	if err != nil {
		return nil, err
	}
	return iv.Store.Heartbeat(ctx, id, *worker, *ttl)
}

func (iv *Invocation) cmdAssignees(ctx context.Context, sub string, args []string) (any, error) {
	if err := parse(iv.flags(sub), args); err != nil {
		return nil, err
	}
	return iv.Store.Assignees(ctx, iv.Board)
}

func (iv *Invocation) cmdContext(ctx context.Context, sub string, args []string) (any, error) {
	if err := parse(iv.flags(sub), args); err != nil {
		return nil, err
	}
	return iv.Store.Context(ctx, iv.Board)
}

func (iv *Invocation) cmdSpecify(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	text := fs.String("text", "", "spec text")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	id, err := iv.taskArg(fs.Args())
	if err != nil {
		return nil, err
	}
	return iv.Store.Specify(ctx, id, *text, iv.Actor)
}

func (iv *Invocation) cmdDecompose(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	max := fs.Int("max", 8, "max children")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	id, err := iv.taskArg(fs.Args())
	if err != nil {
		return nil, err
	}
	return iv.Store.Decompose(ctx, id, iv.Decomposer, *max)
}

func (iv *Invocation) cmdGC(ctx context.Context, sub string, args []string) (any, error) {
	fs := iv.flags(sub)
	age := fs.Duration("done-age", 30*24*time.Hour, "archive done older than")
	if err := parse(fs, args); err != nil {
		return nil, err
	}
	n, err := iv.Store.GC(ctx, iv.Board, *age)
	return map[string]int{"changed": n}, err
}

func (iv *Invocation) cmdRepair(ctx context.Context, sub string, args []string) (any, error) {
	if err := parse(iv.flags(sub), args); err != nil {
		return nil, err
	}
	n, err := iv.Store.Repair(ctx, iv.Board)
	return map[string]int{"fixed": n}, err
}
