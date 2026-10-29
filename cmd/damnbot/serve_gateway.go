package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/telegram"
	"github.com/damnbot/damnbot/internal/heal"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/obs"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/tools"
)

type serveGateway struct {
	cfg                 config.Config
	dataDir, configPath string
	modelRef            string
	store               gwstore.Store
	adapter             *telegram.Adapter
	gate                *gateway.Gate
	quota               *gateway.Quota
	limiter             *gateway.Limiter
	chats               *gateway.ChatQueue
	daemon              *serveDaemon
	metrics             *obs.Metrics
	watchdog            *heal.Watchdog
	supervisor          *heal.Supervisor
	stderr              io.Writer
	inbound             chan telegram.Inbound
	cancel              context.CancelFunc
	pollCancel          context.CancelFunc
	wg                  sync.WaitGroup
	now                 func() time.Time
}

func newServeGateway(ctx context.Context, cfg *config.Config, o *serveOptions, db *ctxdb.Store, daemon *serveDaemon, metrics *obs.Metrics, stderr io.Writer) (*serveGateway, error) {
	token := strings.TrimSpace(os.Getenv("DAMNBOT_TELEGRAM_TOKEN"))
	if token == "" {
		return nil, nil
	}
	owner, allow, err := gatewayEnv()
	if err != nil {
		return nil, err
	}
	modelRef := cmp.Or(strings.TrimSpace(os.Getenv("DAMNBOT_SERVE_MODEL")), cfg.Provider.Model)
	if modelRef == "" {
		return nil, errors.New("gateway: provider.model or DAMNBOT_SERVE_MODEL is required")
	}
	store := gwstore.Store{DB: db}
	for i, spec := range append([]string{owner}, allow...) {
		role := "user"
		if i == 0 {
			role = "owner"
		}
		platform, id, _ := strings.Cut(spec, ":")
		if _, serr := store.EnsureIdentity(ctx, platform, id, role, "platform_authenticated", time.Now().UnixMilli()); serr != nil {
			return nil, fmt.Errorf("gateway: seed %s: %w", role, serr)
		}
		if serr := store.SetStatus(ctx, platform, id, "allowlisted", time.Now().UnixMilli()); serr != nil {
			return nil, fmt.Errorf("gateway: seed %s status: %w", role, serr)
		}
	}
	sup := heal.NewSupervisor(nil)
	wd, err := heal.NewWatchdog(heal.WatchdogOptions{
		Interval: 5 * time.Second, StaleAfter: 90 * time.Second,
		Action: func(context.Context) error { return sup.Degrade("gateway: no successful poll") },
		OnSafeMode: func(reason string) {
			if serr := sup.EnterSafeMode(reason); serr != nil {
				sessionsPrintf(stderr, "gateway: safe mode: %s\n", cleanLine(serr.Error()))
			}
		},
	})
	if err != nil {
		return nil, fmt.Errorf("gateway watchdog: %w", err)
	}
	net := netx.New(netx.Config{Offline: cfg.Network.Offline, NoBackground: !cfg.Network.Background})
	limiter, err := gateway.NewLimiter(gateway.Rate{PerSecond: 1, Burst: 1}, gateway.Rate{PerSecond: 30, Burst: 30})
	if err != nil {
		return nil, fmt.Errorf("gateway limiter: %w", err)
	}
	adapter, err := telegram.New(&telegram.Config{
		Token: token, BaseURL: strings.TrimSpace(os.Getenv("DAMNBOT_TELEGRAM_BASE_URL")),
		Doer: beatDoer{do: telegram.EgressClient(net), wd: wd}, Store: telegramStore{Store: store},
		// EC-PLAT-016: swap the egress client on a confirmed poll stall.
		Rebuild: func() telegram.Doer {
			sessionsPrintf(stderr, "gateway: poll stalled; adapter rebuilt\n")
			metrics.IncError()
			return beatDoer{do: telegram.EgressClient(net), wd: wd}
		},
		Throttle: limiter,
	})
	if err != nil {
		return nil, fmt.Errorf("gateway telegram: %w", err)
	}
	users := make(map[string]string, len(cfg.Gateway.Users))
	for id, u := range cfg.Gateway.Users {
		users[id] = u.Workspace
	}
	gate, err := gateway.New(&gateway.Options{
		DataDir: o.dataDir, Users: users,
		Identities: telegramStore{Store: store}, Inbox: telegramStore{Store: store},
	})
	if err != nil {
		return nil, fmt.Errorf("gateway admission: %w", err)
	}
	quota, err := gateway.NewQuota(gateway.QuotaOptions{})
	if err != nil {
		return nil, fmt.Errorf("gateway quota: %w", err)
	}
	return &serveGateway{
		cfg: *cfg, dataDir: o.dataDir, configPath: o.configPath, modelRef: modelRef,
		store: store, adapter: adapter, gate: gate, quota: quota, limiter: limiter,
		chats: gateway.NewChatQueue(cfg.Gateway.MaxParallelTurns), daemon: daemon,
		metrics: metrics, watchdog: wd, supervisor: sup, stderr: stderr,
		inbound: make(chan telegram.Inbound, 32), now: time.Now,
	}, nil
}

func gatewayEnv() (string, []string, error) {
	owner := strings.TrimSpace(os.Getenv("DAMNBOT_GATEWAY_OWNER"))
	if owner == "" {
		return "", nil, errors.New("gateway: DAMNBOT_GATEWAY_OWNER=platform:id is required when a bot token is set")
	}
	if platform, id, ok := strings.Cut(owner, ":"); !ok || !gwstore.ValidIdentity(platform) || !gwstore.ValidIdentity(id) {
		return "", nil, fmt.Errorf("gateway: DAMNBOT_GATEWAY_OWNER=%q is not platform:id", cleanArg(owner))
	}
	var allow []string
	for item := range strings.SplitSeq(os.Getenv("DAMNBOT_GATEWAY_ALLOW"), ",") {
		if item = strings.TrimSpace(item); item == "" {
			continue
		}
		if platform, id, ok := strings.Cut(item, ":"); !ok || !gwstore.ValidIdentity(platform) || !gwstore.ValidIdentity(id) {
			return "", nil, fmt.Errorf("gateway: DAMNBOT_GATEWAY_ALLOW entry %q is not platform:id", cleanArg(item))
		}
		allow = append(allow, item)
	}
	return owner, allow, nil
}

func (g *serveGateway) Start(ctx context.Context) error {
	if g == nil {
		return nil
	}
	if err := g.supervisor.Start(); err != nil {
		return fmt.Errorf("gateway: supervisor start: %w", err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	pollCtx, pollCancel := context.WithCancel(runCtx)
	g.cancel, g.pollCancel = cancel, pollCancel
	g.wg.Add(4)
	go func() {
		defer g.wg.Done()
		if err := g.adapter.Run(pollCtx, g.inbound); err != nil && pollCtx.Err() == nil {
			sessionsPrintf(g.stderr, "gateway: poller stopped: %s\n", cleanLine(err.Error()))
			g.metrics.IncError()
		}
	}()
	go func() {
		defer g.wg.Done()
		for {
			select {
			case <-runCtx.Done():
				return
			case in := <-g.inbound:
				g.watchdog.Beat()
				g.metrics.SetQueue(len(g.inbound))
				update := in
				chatKey := update.Platform + "\x00" + update.ChatID
				if err := g.chats.Submit(runCtx, chatKey, func(ctx context.Context) { g.handle(ctx, &update) }); err != nil && runCtx.Err() == nil {
					sessionsPrintf(g.stderr, "gateway: dispatch %s: %s\n", cleanArg(update.UpdateID), cleanLine(err.Error()))
					g.metrics.IncError()
				}
			}
		}
	}()
	go func() { defer g.wg.Done(); g.deliveries(runCtx) }()
	go func() { defer g.wg.Done(); g.watchdog.Run(runCtx) }()
	return nil
}

func (g *serveGateway) Stop() {
	if g != nil && g.cancel != nil {
		g.cancel()
		g.chats.Close()
		g.wg.Wait()
	}
}

func (g *serveGateway) handle(ctx context.Context, in *telegram.Inbound) {
	if fields := strings.Fields(in.Text); len(fields) > 0 {
		if command, _, _ := strings.Cut(fields[0], "@"); command == "/pair" {
			code := ""
			if len(fields) > 1 {
				code = fields[1]
			}
			g.pair(ctx, in, code)
			return
		}
	}
	inbound := &gateway.Inbound{
		Platform: in.Platform, ChatID: in.ChatID, ThreadID: in.ThreadID, SenderID: in.SenderID,
		UpdateID: in.UpdateID, Group: strings.HasPrefix(in.ChatID, "-"), Payload: `{"kind":"text","text":` + strconv.Quote(in.Text) + `}`,
	}
	out, err := g.gate.Admit(ctx, inbound, func(wctx context.Context, t gateway.Ticket) error { return g.turn(wctx, in, &t) })
	if err != nil {
		sessionsPrintf(g.stderr, "gateway: update %s: %s\n", cleanArg(in.UpdateID), cleanLine(err.Error()))
		g.metrics.IncError()
	} else if out.Kind == gateway.Rejected {
		g.metrics.IncError()
	}
}

func (g *serveGateway) pair(ctx context.Context, in *telegram.Inbound, code string) {
	text := "pairing failed: invalid code"
	if code != "" {
		text = "pairing failed"
		if _, err := g.store.RedeemPairing(ctx, "telegram", code, in.SenderID, "platform_authenticated", time.Now().UnixMilli()); err == nil {
			text = "pairing complete"
		}
	}
	if _, err := g.adapter.Send(ctx, &telegram.Outbound{ChatID: in.ChatID, ThreadID: in.ThreadID, Text: text}); err != nil {
		g.metrics.IncError()
	}
}

func (g *serveGateway) turn(ctx context.Context, in *telegram.Inbound, t *gateway.Ticket) error {
	req := gateway.QuotaRequest{
		Platform: t.Scope.Platform, Identity: t.Scope.UserID,
		ChatID: t.Scope.ChatID, Group: strings.HasPrefix(t.Scope.ChatID, "-"),
	}
	res, err := g.quota.Take(req)
	if err != nil {
		return fmt.Errorf("gateway: quota: %w", err)
	}
	if res.Limit != gateway.LimitNone {
		g.metrics.IncError()
		if g.quota.Notice(req) {
			_, serr := g.adapter.Send(ctx, &telegram.Outbound{ChatID: in.ChatID, ThreadID: in.ThreadID, Text: res.Message()})
			if serr != nil {
				return fmt.Errorf("gateway: quota notice: %w", serr)
			}
		}
		return nil
	}
	_, end, ok := g.daemon.BeginTurn()
	if !ok {
		return errors.New("gateway: daemon is draining")
	}
	defer end()
	typing := g.startTyping(ctx, in.ChatID, in.ThreadID)
	defer typing.end()
	ack := g.placeholder(ctx, in)
	start := time.Now()
	sid, err := g.session(ctx, t)
	if err == nil {
		err = g.runChat(ctx, in, sid, typing, ack)
	}
	if err != nil {
		g.failPlaceholder(ctx, in, ack)
	}
	g.metrics.Turn(time.Since(start), err == nil)
	return err
}

func (g *serveGateway) session(ctx context.Context, t *gateway.Ticket) (string, error) {
	route := gwstore.Route{Platform: t.Scope.Platform, ChatID: t.Scope.ChatID, ThreadID: t.Scope.ThreadID}
	principal := t.Scope.Label()
	sid, found, err := g.store.Session(ctx, route, principal)
	if err != nil {
		return "", fmt.Errorf("gateway: session: %w", err)
	}
	if found {
		return sid, nil
	}
	if sid, err = newSessionID(); err != nil {
		return "", err
	}
	cwd := t.Scope.Workspace
	if cwd == "" {
		if cwd, err = os.Getwd(); err != nil {
			return "", fmt.Errorf("gateway: getwd: %w", err)
		}
	}
	if _, cerr := g.store.DB.CreateSession(ctx, ctxdb.Session{
		ID: sid, Origin: "gateway", Cwd: cwd, Model: g.modelRef,
		ConfigSHA: hashConfig(&g.cfg), GatewayIdentity: principal,
	}); cerr != nil {
		return "", fmt.Errorf("gateway: create session: %w", cerr)
	}
	err = g.store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`INSERT INTO gateway_conversations(platform, chat_id, thread_id, session_id, created_at)
			VALUES (?1, ?2, ?3, ?4, ?5) ON CONFLICT DO NOTHING`,
			route.Platform, route.ChatID, route.ThreadID, sid, time.Now().UnixMilli())
		if xerr != nil {
			return fmt.Errorf("bind route: %w", xerr)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("gateway: bind session: %w", err)
	}
	return sid, nil
}

func (g *serveGateway) runChat(ctx context.Context, in *telegram.Inbound, sid string, typing *gatewayTyping, ack *gatewayAck) error {
	o := runOptions{
		prompt: in.Text, model: g.modelRef, approval: "ask", configPath: g.configPath, dataDir: g.dataDir,
		baseURL: strings.TrimSpace(os.Getenv("DAMNBOT_SERVE_BASE_URL")), apiKey: strings.TrimSpace(os.Getenv("DAMNBOT_SERVE_API_KEY")),
		session: sid, approvalOrigin: "run", cfgOverride: &g.cfg,
	}
	o.approver = gatewayLoopApprover{g: g, chat: in.ChatID, thread: in.ThreadID}
	o.toolApprover = gatewayToolApprover{g: g, chat: in.ChatID, thread: in.ThreadID}
	cfg, dataDir, configDir, home, err := loadRunEnv(&o)
	if err != nil {
		return err
	}
	red := redact.New(redact.Options{})
	codec, err := runCodec(ctx, &cfg, dataDir, configDir)
	if err != nil {
		return err
	}
	model, err := startRunModel(ctx, &cfg, dataDir, o.model, &o, red, configDir, home, codec)
	if err != nil {
		return err
	}
	eng, err := newTurnEngine(ctx, &cfg, &o, model, o.model, red, model.spill)
	if err != nil {
		return errors.Join(err, model.close(ctx))
	}
	defer func() {
		if cerr := model.close(ctx); cerr != nil {
			sessionsPrintf(g.stderr, "gateway: close turn: %s\n", cleanLine(cerr.Error()))
		}
	}()
	eng.disp.BeginTurn()
	if perr := applyPromptRefs(ctx, eng.disp, &o, model); perr != nil {
		return perr
	}
	ch, err := eng.loop.Prompt(ctx, sid, loop.Input{Text: in.Text})
	if err != nil {
		return fmt.Errorf("gateway: prompt: %w", err)
	}
	return g.streamReply(ctx, in, ch, typing, ack)
}

// Reply cadence: the first delta opens the Telegram message immediately, later
// deltas coalesce into one update. An update needs replyEditGap since the
// previous one (the adapter's own limiter stays the final authority for I/O),
// and then either replyFlushEvery of text or replyFlushChars of new text,
// whichever comes first. The final update is always the full answer.
const (
	replyEditGap    = 1100 * time.Millisecond
	replyFlushEvery = 1200 * time.Millisecond
	replyFlushChars = 80
	// replyShortChars caps mid-stream edits: a reply at or below this size gets
	// exactly one final edit, however long the model takes to produce it.
	replyShortChars = 120
	// gatewayAckText is the instant placeholder shown right after admission;
	// it is edited into the answer and never left behind.
	gatewayAckText    = "…"
	gatewayNoOutput   = "damnbot: no output"
	gatewayTurnFailed = "damnbot: turn failed"
)

// replyPacer decides when accumulated reply text becomes one Telegram update.
type replyPacer struct {
	started bool
	last    time.Time
	flushed int
	edits   int
}

// observe records total reply bytes at now and reports whether they should be
// flushed. Short replies never flush mid-stream (their single final edit is
// enough); longer ones keep the ≥1.1 s / 1.2 s-or-80-chars cadence.
func (p *replyPacer) observe(now time.Time, total int) bool {
	switch {
	case total == 0 || total == p.flushed:
		return false
	case total <= replyShortChars:
		return false
	case !p.started:
		p.started, p.last, p.flushed = true, now, total
		return false
	}
	gap := now.Sub(p.last)
	if gap < replyEditGap {
		return false
	}
	if gap < replyFlushEvery && total-p.flushed < replyFlushChars {
		return false
	}
	p.last, p.flushed = now, total
	p.edits++
	return true
}

// replyOutcome is the final state of one streamed reply.
type replyOutcome struct {
	Text   string
	Failed bool
	Detail event.StopDetail
	Err    error
}

// streamReplyUpdates drains the turn events, flushing the growing answer at the
// reply cadence, and always flushes the final text exactly once: the full
// accumulated answer, or the provider-error message when the turn failed. wait
// paces the final flush so no two updates reach the adapter closer than
// replyEditGap; it is injected so tests advance a fake clock instead of sleeping.
func streamReplyUpdates(ctx context.Context, ch <-chan event.Update, now func() time.Time, wait func(context.Context, time.Duration) error, flush func(context.Context, string) error, start time.Time, shown int) replyOutcome {
	var answer []byte
	pacer := replyPacer{started: shown > 0, last: start, flushed: shown}
	var stop event.StopReason
	var detail event.StopDetail
	var sent string
	var sendErr error
	for up := range ch {
		switch up.Kind {
		case event.UpdateTextDelta:
			answer = append(answer, up.Text...)
			if sendErr == nil && pacer.observe(now(), len(answer)) {
				sent = string(answer)
				sendErr = flush(ctx, sent)
			}
		case event.UpdateStop:
			stop, detail = up.Stop, up.Detail
		case event.UpdateToolCallStart, event.UpdateToolCallArgs, event.UpdateToolCallEnd, event.UpdateToolResult, event.UpdateUsage, event.UpdateNotice:
		}
	}
	failed := stop == event.StopError || detail == event.DetailProviderError
	text := string(answer)
	if failed {
		text = "damnbot: provider error (" + string(detail) + ")"
	}
	out := replyOutcome{Text: text, Failed: failed, Detail: detail}
	if text == "" {
		return out
	}
	if pacer.edits > 0 && text != sent {
		if gap := replyEditGap - now().Sub(pacer.last); gap > 0 {
			if werr := wait(ctx, gap); werr != nil {
				out.Err = werr
				return out
			}
		}
	}
	if ferr := flush(ctx, text); ferr != nil {
		out.Err = ferr
	}
	return out
}

// flushTyped updates the streamed message and stops the typing indicator once
// the first visible reply is delivered.
func flushTyped(ctx context.Context, reply *telegram.Stream, typing *gatewayTyping, text string) error {
	if err := reply.Update(ctx, text); err != nil {
		return fmt.Errorf("gateway: reply update: %w", err)
	}
	if typing != nil {
		typing.halt()
	}
	return nil
}

// gatewayAck is the instant placeholder message shown after admission. The
// same message is later edited into the answer, so a turn never leaves a
// second message behind.
type gatewayAck struct {
	id       int64
	replaced bool
}

// placeholder sends the immediate "…" acknowledgement. Failure is not fatal:
// the turn falls back to opening the reply message on its final text.
func (g *serveGateway) placeholder(ctx context.Context, in *telegram.Inbound) *gatewayAck {
	key := gateway.DeliveryKey("telegram", in.ChatID, "reply:"+in.UpdateID)
	id, err := g.adapter.Send(ctx, &telegram.Outbound{ChatID: in.ChatID, ThreadID: in.ThreadID, Text: gatewayAckText, Key: key})
	if err != nil {
		g.metrics.IncError()
		return nil
	}
	return &gatewayAck{id: id}
}

// failPlaceholder turns the placeholder into an error message when the turn
// failed before any reply update replaced it.
func (g *serveGateway) failPlaceholder(ctx context.Context, in *telegram.Inbound, ack *gatewayAck) {
	if ack == nil || ack.replaced {
		return
	}
	key := gateway.DeliveryKey("telegram", in.ChatID, "reply:"+in.UpdateID)
	reply := g.adapter.StreamFrom(&telegram.Outbound{ChatID: in.ChatID, ThreadID: in.ThreadID, Key: key}, ack.id, gatewayAckText)
	if err := reply.Update(ctx, gatewayTurnFailed); err != nil {
		g.metrics.IncError()
	}
}

func (g *serveGateway) streamReply(ctx context.Context, in *telegram.Inbound, ch <-chan event.Update, typing *gatewayTyping, ack *gatewayAck) error {
	key := gateway.DeliveryKey("telegram", in.ChatID, "reply:"+in.UpdateID)
	out := &telegram.Outbound{ChatID: in.ChatID, ThreadID: in.ThreadID, Key: key}
	reply := g.adapter.Stream(out)
	shown := 0
	if ack != nil {
		reply = g.adapter.StreamFrom(out, ack.id, gatewayAckText)
		shown = len(gatewayAckText)
	}
	flush := func(ctx context.Context, text string) error {
		if err := flushTyped(ctx, reply, typing, text); err != nil {
			return err
		}
		if ack != nil {
			ack.replaced = true
		}
		return nil
	}
	now := g.now
	if now == nil {
		now = time.Now
	}
	res := streamReplyUpdates(ctx, ch, now, waitReply, flush, now(), shown)
	if res.Text == "" && ack != nil && !ack.replaced {
		res.Text = gatewayNoOutput
		if ferr := flush(ctx, res.Text); ferr != nil {
			res.Err = ferr
		}
	}
	if res.Err != nil {
		if res.Text != "" {
			// The reply key already stores the placeholder message id, so a
			// retry under it would look "sent"; the outbox gets its own key and
			// delivers the final text as a fresh message when editing failed.
			finalKey := gateway.DeliveryKey("telegram", in.ChatID, "final:"+in.UpdateID)
			if _, _, qerr := g.store.EnqueueDelivery(ctx, &gwstore.Delivery{IdemKey: finalKey, Platform: "telegram", ChatID: in.ChatID, ThreadID: in.ThreadID, Body: res.Text, Status: gateway.StatusQueued}); qerr != nil {
				return errors.Join(res.Err, fmt.Errorf("gateway: enqueue reply: %w", qerr))
			}
		}
		g.metrics.IncError()
	}
	if res.Failed {
		return fmt.Errorf("gateway: turn failed: %s", res.Detail)
	}
	return nil
}

// waitReply paces the final reply update without sleeping past ctx.
func waitReply(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("gateway: final reply wait: %w", ctx.Err())
	case <-t.C:
		return nil
	}
}

// typingEvery repeats the chat action while a turn waits for its first visible
// reply; Telegram expires the indicator after ~5 s of silence.
const typingEvery = 4 * time.Second

// gatewayTyping owns the best-effort chat action loop of one turn.
type gatewayTyping struct {
	stop chan struct{}
	once sync.Once
	wg   sync.WaitGroup
}

// startTyping sends the first typing action immediately, before the model is
// called, and repeats it until halt, the turn context ends, or the first reply
// update is delivered. Errors are ignored: a rejected action never fails a turn.
func (g *serveGateway) startTyping(ctx context.Context, chat, thread string) *gatewayTyping {
	t := &gatewayTyping{stop: make(chan struct{})}
	t.wg.Go(func() {
		for {
			g.adapter.Typing(ctx, chat, thread) //nolint:errcheck,gosec // best effort: never fails the turn
			timer := time.NewTimer(typingEvery)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-t.stop:
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	})
	return t
}

// halt stops the loop without waiting; end also waits for it to exit.
func (t *gatewayTyping) halt() { t.once.Do(func() { close(t.stop) }) }
func (t *gatewayTyping) end()  { t.halt(); t.wg.Wait() }

type gatewayToolApprover struct {
	g            *serveGateway
	chat, thread string
}

func (a gatewayToolApprover) Approve(ctx context.Context, req *tools.ApprovalRequest) (tools.Approval, error) {
	allow, err := a.g.feedback(ctx, a.chat, a.thread, req.Tool, req.Preview)
	if err != nil || !allow {
		return tools.ApprovalDeny, err
	}
	return tools.ApprovalAllowOnce, nil
}

type gatewayLoopApprover struct {
	g            *serveGateway
	chat, thread string
}

func (a gatewayLoopApprover) Approve(ctx context.Context, req *loop.ApprovalRequest) (loop.ApprovalResult, error) {
	allow, err := a.g.feedback(ctx, a.chat, a.thread, req.Tool, req.Preview)
	if err != nil {
		return loop.ApprovalResult{Decision: loop.DecisionDeny}, err
	}
	if allow {
		return loop.ApprovalResult{Decision: loop.DecisionAllowOnce}, nil
	}
	return loop.ApprovalResult{Decision: loop.DecisionDeny}, nil
}

func (g *serveGateway) feedback(ctx context.Context, chat, thread, tool, preview string) (bool, error) {
	preview = cmp.Or(preview, tool)
	d, err := g.adapter.Approve(ctx, chat, thread, preview)
	if err != nil {
		return false, fmt.Errorf("gateway: approval: %w", err)
	}
	return d == telegram.DecisionAllow, nil
}

// deliveryMessage builds the outbound message of one claimed delivery,
// resolving media_blob_sha when the row carries an attachment.
func (g *serveGateway) deliveryMessage(ctx context.Context, d *gwstore.Delivery) (*telegram.Outbound, error) {
	out := &telegram.Outbound{ChatID: d.ChatID, ThreadID: d.ThreadID, Text: d.Body, Key: d.IdemKey}
	if d.MediaSHA == "" {
		return out, nil
	}
	media, err := g.loadMedia(ctx, d.MediaSHA)
	if err != nil {
		return nil, err
	}
	out.Media = media
	return out, nil
}

// loadMedia resolves gateway_deliveries.media_blob_sha to the bytes and MIME
// the adapter uploads. The blob is owned by the delivery row; a missing or
// expired blob is retried through the normal delivery backoff, never sent as
// an empty file.
func (g *serveGateway) loadMedia(ctx context.Context, sha string) (*telegram.Media, error) {
	var mimeType string
	if err := g.store.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT mime FROM blobs WHERE sha256 = ?1`, []any{sha}, &mimeType)
	}); err != nil {
		return nil, fmt.Errorf("media blob %s: %w", sha, err)
	}
	data, err := g.store.DB.ReadBlob(ctx, sha)
	if err != nil {
		return nil, fmt.Errorf("media blob %s: %w", sha, err)
	}
	return &telegram.Media{MIME: mimeType, Data: data}, nil
}

type beatDoer struct {
	do telegram.Doer
	wd *heal.Watchdog
}

func (b beatDoer) Do(req *http.Request) (*http.Response, error) {
	resp, err := b.do.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gateway egress: %w", err)
	}
	b.wd.Beat()
	return resp, nil
}

type telegramStore struct{ gwstore.Store }

func (s telegramStore) Offset(ctx context.Context, adapter string) (int64, error) {
	var n int64
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT CAST(cursor AS INTEGER) FROM gateway_cursors WHERE platform = ?1 AND account = ?2`, []any{"telegram", adapter}, &n)
	})
	if errors.Is(err, ctxdb.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("gateway: load offset: %w", err)
	}
	return n, nil
}

func (s telegramStore) PutOffset(ctx context.Context, adapter string, next int64) error {
	err := s.DB.Write(ctx, func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`INSERT INTO gateway_cursors(platform, account, cursor, updated_at) VALUES (?1, ?2, ?3, ?4)
			ON CONFLICT(platform, account) DO UPDATE SET cursor = excluded.cursor, updated_at = excluded.updated_at`,
			"telegram", adapter, strconv.FormatInt(next, 10), time.Now().UnixMilli())
		if xerr != nil {
			return fmt.Errorf("put offset: %w", xerr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("gateway: persist offset: %w", err)
	}
	return nil
}

func (s telegramStore) ClaimCallback(ctx context.Context, id string) (bool, error) {
	err := s.Store.ClaimCallback(ctx, id)
	if err != nil && !errors.Is(err, gwstore.ErrAlreadyClaimed) {
		return false, fmt.Errorf("gateway: claim callback: %w", err)
	}
	return err == nil, nil
}

func (s telegramStore) Identity(ctx context.Context, platform, externalID string) (gateway.Identity, bool, error) {
	id, found, err := s.Lookup(ctx, platform, externalID)
	if err != nil {
		return gateway.Identity{}, false, fmt.Errorf("gateway: identity: %w", err)
	}
	return gateway.Identity{
		Platform: platform, ExternalID: externalID, DisplayName: id.DisplayName,
		Role: gateway.Role(id.Role), Status: gateway.Status(id.Status), Strength: gateway.Strength(id.Strength), Workspace: id.Workspace,
	}, found, nil
}

func (s telegramStore) Record(ctx context.Context, in *gateway.Inbound) (bool, error) {
	ok, err := s.Store.Record(ctx, &gwstore.Inbound{
		Platform: in.Platform, ChatID: in.ChatID, ThreadID: in.ThreadID, SenderID: in.SenderID,
		UpdateID: in.UpdateID, Payload: in.Payload,
	})
	if err != nil {
		return false, fmt.Errorf("gateway: record inbound: %w", err)
	}
	return ok, nil
}
