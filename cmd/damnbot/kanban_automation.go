//go:build !minimal

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/kanban"
)

const (
	kanbanGatewayBoard   = "default"
	kanbanGatewayWorker  = "gateway"
	kanbanWatchEvery     = time.Second
	kanbanDispatchEvery  = 5 * time.Second
	kanbanNotifyTailSize = 200
	kanbanSummaryMax     = 4000
)

// kanbanSender is the production Sender: it enqueues the markdown body in the
// durable gateway outbox. The adapter renderer and the outq pacing therefore
// apply exactly as for a chat reply; no raw fmt string reaches a platform.
type kanbanSender struct{ g *serveGateway }

func (s kanbanSender) Send(ctx context.Context, platform, chat, key, body string) error {
	if s.g == nil {
		return errors.New("kanban: sender needs the gateway")
	}
	if strings.TrimSpace(body) == "" {
		return nil
	}
	s.g.notifyChat(ctx, platform, chat, key, body)
	return nil
}

// startKanbanAutomation starts the opt-in [kanban] loops of AUTO-017..019.
// Every switch is off by default, so a gateway without [kanban] does no board
// work at all. The caller owns ctx and the wait group.
func startKanbanAutomation(ctx context.Context, g *serveGateway, db *ctxdb.Store) {
	if g == nil || db == nil {
		return
	}
	cfg := g.cfg.Kanban
	if !cfg.DispatchInGateway && !cfg.AutoDecompose && !cfg.NotifyInGateway {
		return
	}
	workspace, err := os.Getwd()
	if err != nil {
		workspace = ""
	}
	store := kanban.New(db, workspace)
	if cfg.NotifyInGateway {
		w := &kanbanWatcher{
			store:    store,
			notifier: &Notifier{Store: store, Sender: kanbanSender{g: g}},
			stderr:   g.stderr,
		}
		g.wg.Go(func() {
			w.Run(ctx)
		})
	}
	if !cfg.DispatchInGateway && !cfg.AutoDecompose {
		return
	}
	d := &kanban.Dispatcher{
		Store: store, Board: kanbanGatewayBoard, AutoDecompose: cfg.AutoDecompose,
		Decomposer: &kanbanLLMDecomposer{kanbanLLMTurn: kanbanLLMTurn{configPath: g.configPath, dataDir: g.dataDir, model: g.modelRef, workspace: workspace, stderr: g.stderr, baseURL: serveBaseURL(), apiKey: serveAPIKey()}},
		Log: func(msg string) {
			sessionsPrintf(g.stderr, "kanban: dispatcher: %s\n", cleanLine(msg))
		},
	}
	if cfg.DispatchInGateway {
		d.Workers = []string{kanbanGatewayWorker}
		d.Runner = &kanbanLLMWorker{kanbanLLMTurn: kanbanLLMTurn{configPath: g.configPath, dataDir: g.dataDir, model: g.modelRef, workspace: workspace, stderr: g.stderr, baseURL: serveBaseURL(), apiKey: serveAPIKey()}}
	}
	g.wg.Go(func() {
		if err := d.Run(ctx, kanbanDispatchEvery); err != nil && !errors.Is(err, context.Canceled) {
			sessionsPrintf(g.stderr, "kanban: dispatcher stop: %s\n", cleanLine(err.Error()))
		}
	})
}

// kanbanWatcher turns append-only board events into notifications. It starts
// at the current head, so a restart never replays history; the Notifier owns
// the 1/min/task rate limit.
type kanbanWatcher struct {
	store    *kanban.Store
	notifier *Notifier
	stderr   io.Writer
	cursor   int64
	lastErr  string
}

func (w *kanbanWatcher) Run(ctx context.Context) {
	if evs, err := w.store.Tail(ctx, kanbanGatewayBoard, 1); err == nil && len(evs) > 0 {
		w.cursor = evs[0].ID
	}
	ticker := time.NewTicker(kanbanWatchEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *kanbanWatcher) tick(ctx context.Context) {
	evs, err := w.store.Tail(ctx, kanbanGatewayBoard, kanbanNotifyTailSize)
	if err != nil {
		w.logOnce(err)
		return
	}
	w.lastErr = ""
	// Tail is newest-first: emit oldest-first and only what is past the cursor.
	for i := range slices.Backward(evs) {
		ev := evs[i]
		if ev.ID <= w.cursor {
			continue
		}
		w.cursor = ev.ID
		task, terr := w.store.Task(ctx, ev.TaskID)
		if terr != nil {
			w.logOnce(terr)
			continue
		}
		if nerr := w.notifier.Notify(ctx, task, kanbanEventChange(&ev)); nerr != nil {
			w.logOnce(nerr)
		}
	}
}

func (w *kanbanWatcher) logOnce(err error) {
	msg := cleanLine(err.Error())
	if msg == w.lastErr || w.stderr == nil {
		return
	}
	w.lastErr = msg
	sessionsPrintf(w.stderr, "kanban: watcher: %s\n", msg)
}

// kanbanEventChange renders one event as a one-line change for the notice.
func kanbanEventChange(ev *kanban.Event) string {
	var p kanban.Payload
	if len(ev.Payload) > 0 && json.Unmarshal(ev.Payload, &p) != nil {
		return ev.Kind
	}
	switch {
	case p.From != "" && p.To != "":
		return fmt.Sprintf("%s → %s", p.From, p.To)
	case p.Text != "":
		return "comment: " + cleanLine(p.Text)
	default:
		return ev.Kind
	}
}

// serveBaseURL/serveAPIKey mirror the gateway chat turn: the serve env
// override wins, else the provider table in the config file applies.
func serveBaseURL() string { return strings.TrimSpace(config.OS().Getenv("DAMNBOT_SERVE_BASE_URL")) }
func serveAPIKey() string  { return strings.TrimSpace(config.OS().Getenv("DAMNBOT_SERVE_API_KEY")) }

// kanbanLLMTurn is one headless turn with the serve model and route.
type kanbanLLMTurn struct {
	configPath, dataDir, model, workspace, baseURL, apiKey string
	stderr                                                 io.Writer
}

func (t *kanbanLLMTurn) turn(ctx context.Context, prompt string) (string, error) {
	var out bytes.Buffer
	code, err := runOne(ctx, &runOptions{
		prompt: prompt, model: t.model, configPath: t.configPath, dataDir: t.dataDir,
		output: "text", cwd: t.workspace, sessionOrigin: "kanban", approvalOrigin: "run", approval: "ask",
		baseURL: t.baseURL, apiKey: t.apiKey,
	}, &out, t.stderr)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("kanban worker: turn exit %d", code)
	}
	return strings.TrimSpace(out.String()), nil
}

// kanbanLLMWorker runs one claimed card as a headless turn. The board owns the
// lease and the run record; the worker only returns the handoff.
type kanbanLLMWorker struct{ kanbanLLMTurn }

func (w *kanbanLLMWorker) Run(ctx context.Context, t *kanban.Task, worker string) (kanban.WorkerOutcome, error) {
	summary, err := w.runTurn(ctx, kanbanWorkerPrompt(t))
	if err != nil {
		return kanban.WorkerOutcome{}, err
	}
	return kanban.WorkerOutcome{
		Outcome: "ok",
		Handoff: kanban.Handoff{Summary: summary},
		Run:     &kanban.Run{ID: kanban.RunIDFor(t.ID, worker, summary), Worker: worker, Outcome: "ok"},
	}, nil
}

func (w *kanbanLLMWorker) runTurn(ctx context.Context, prompt string) (string, error) {
	summary, err := w.turn(ctx, prompt)
	if err != nil {
		return "", err
	}
	if summary == "" {
		summary = "completed"
	}
	if len(summary) > kanbanSummaryMax {
		summary = summary[:kanbanSummaryMax]
	}
	return summary, nil
}

func kanbanWorkerPrompt(t *kanban.Task) string {
	var b strings.Builder
	b.WriteString("You are the kanban worker for this board task. Complete it, then reply with a short handoff summary.\n\n")
	b.WriteString("Task " + t.ID + " — " + cleanLine(t.Title) + "\n")
	if t.Body != "" {
		b.WriteString("\n" + t.Body + "\n")
	}
	if t.Spec != "" {
		b.WriteString("\nSpec:\n" + t.Spec + "\n")
	}
	if len(t.Links) > 0 {
		b.WriteString("\nLinked tasks: " + strings.Join(t.Links, ", ") + "\n")
	}
	return b.String()
}

// kanbanLLMDecomposer is the AUTO-018 auxiliary call: one headless turn that
// must return the child plan as JSON. The store validates the schema and bills
// the returned run id once.
type kanbanLLMDecomposer struct {
	kanbanLLMTurn
	turnFn func(context.Context, string) (string, error)
}

func (d *kanbanLLMDecomposer) Decompose(ctx context.Context, t *kanban.Task, max int) (kanban.Plan, kanban.Usage, error) {
	run := d.turnFn
	if run == nil {
		run = d.turn
	}
	raw, err := run(ctx, kanbanDecomposePrompt(t, max))
	if err != nil {
		return kanban.Plan{}, kanban.Usage{}, err
	}
	var plan kanban.Plan
	if err := json.Unmarshal([]byte(kanbanJSONObject(raw)), &plan); err != nil {
		return kanban.Plan{}, kanban.Usage{}, fmt.Errorf("decompose plan: %w", err)
	}
	return plan, kanban.Usage{RunID: kanban.RunIDFor(t.ID, raw)}, nil
}

func kanbanDecomposePrompt(t *kanban.Task, max int) string {
	return fmt.Sprintf("Split the kanban task below into at most %d child tasks. "+
		"Reply with JSON only, shaped {\"children\":[{\"title\":\"…\",\"body\":\"…\"}]}.\n\n"+
		"Task %s — %s\n\n%s\n", max, t.ID, cleanLine(t.Title), t.Body)
}

// kanbanJSONObject drops markdown fences and prose around the JSON object the
// auxiliary model returned, so a chatty answer still reaches the validator.
func kanbanJSONObject(raw string) string {
	start := strings.Index(raw, "{")
	end := strings.LastIndex(raw, "}")
	if start < 0 || end <= start {
		return raw
	}
	return raw[start : end+1]
}
