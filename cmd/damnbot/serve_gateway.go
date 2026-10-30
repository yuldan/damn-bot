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
	"sync/atomic"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/cron"
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
	estop               gateway.Estop
	adapter             *telegram.Adapter
	gate                *gateway.Gate
	quota               *gateway.Quota
	limiter             *gateway.Limiter
	chats               *gateway.ChatQueue
	registry            *gateway.Registry
	daemon              *serveDaemon
	metrics             *obs.Metrics
	watchdog            *heal.Watchdog
	supervisor          *heal.Supervisor
	cron                *cron.Recorder
	cronAdmin           *cron.Admin
	stderr              io.Writer
	inbound             chan telegram.Inbound
	cancel              context.CancelFunc
	pollCancel          context.CancelFunc
	wg                  sync.WaitGroup
	now                 func() time.Time
	started             time.Time
	delayRandom         func() time.Duration
	delayWait           func(context.Context, time.Duration) error
	restartCh           chan struct{}
	restartOnce         sync.Once
	restartPending      atomic.Bool
	runDone             <-chan struct{}
	// run is the one chat turn. It is a field so tests can substitute a fake
	// runner: no command in this file needs a provider or a token.
	run func(context.Context, *telegram.Inbound, *gateway.Ticket) error

	// turns maps the live turn of each (route, principal) to its cancel, so
	// /stop cancels exactly that chat's turn and never a sibling's.
	turnMu sync.Mutex
	turns  map[turnKey]*chatTurn

	// owner is the platform:id of the owner identity: the destination of
	// `__system__` notices (thread lost, poll conflict, crash pause).
	owner string

	// waitMu guards waits, the start time of every turn still waiting for its
	// first provider update (REL-07 "menunggu provider N s").
	waitMu sync.Mutex
	waits  map[string]time.Time
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
	// A crash between claim and SentDelivery leaves a row in 'sending'. This
	// process owns the data dir (serve flock), so every such row is orphaned:
	// requeue it before the delivery worker starts (SELF-HEALING §3.4).
	if rerr := reclaimOnStart(ctx, db, stderr); rerr != nil {
		return nil, rerr
	}
	if serr := seedGatewayIdentities(ctx, store, owner, allow); serr != nil {
		return nil, serr
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

	var self *serveGateway
	adapter, limiter, err := newGatewayAdapter(ctx, token, net, store, wd, metrics, stderr, func(ctx context.Context, key, body string) {
		if self != nil {
			self.notifyOwner(ctx, key, body)
		}
	})
	if err != nil {
		return nil, err
	}
	users := gatewayUsers(cfg)
	quota, err := gateway.NewQuota(gateway.QuotaOptions{})
	if err != nil {
		return nil, fmt.Errorf("gateway quota: %w", err)
	}
	g := &serveGateway{
		cfg: *cfg, dataDir: o.dataDir, configPath: o.configPath, modelRef: modelRef,
		store: store, estop: gateway.Estop{Dir: o.dataDir}, adapter: adapter, quota: quota, limiter: limiter,
		chats: gateway.NewChatQueue(cfg.Gateway.MaxParallelTurns), daemon: daemon,
		metrics: metrics, watchdog: wd, supervisor: sup, stderr: stderr,
		inbound: make(chan telegram.Inbound, 32), now: time.Now, owner: owner,
		turns: make(map[turnKey]*chatTurn), waits: make(map[string]time.Time),
		restartCh: make(chan struct{}, 1),
	}
	self = g
	if rerr := g.startupRecovery(ctx, db, stderr); rerr != nil {
		return nil, rerr
	}
	if rerr := g.registerPlatform(); rerr != nil {
		return nil, rerr
	}
	g.delayRandom = func() time.Duration { return time.Duration(time.Now().UnixNano()) % (humanDelayMax - humanDelayMin) }
	g.delayWait = waitReply
	g.started = g.now()
	g.run = g.turn
	g.wireCronAdmin(db, stderr)
	commands := gateway.NewCommands(&gateway.CommandOptions{Runtime: g, Settings: store, Control: g, Estop: &g.estop})
	gate, err := newServeGate(o, store, users, cfg.Budget.GatewayUserDailyUSD, db, commands, &g.estop)
	if err != nil {
		return nil, err
	}
	g.gate = gate
	return g, nil
}

// newGatewayAdapter owns the one Telegram adapter and its outbound limiter.
// notify receives the 409 conflict notice (REL-02).
func newGatewayAdapter(ctx context.Context, token string, net *netx.Network, store gwstore.Store, wd *heal.Watchdog, metrics *obs.Metrics, stderr io.Writer, notify func(context.Context, string, string)) (*telegram.Adapter, *gateway.Limiter, error) {
	limiter, err := gateway.NewLimiterCaps(gateway.TelegramCaps())
	if err != nil {
		return nil, nil, fmt.Errorf("gateway limiter: %w", err)
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
		Throttle:          limiter,
		MaxChunksPerReply: gateway.TelegramCaps().MaxChunksPerReply,
		Scan:              net.ScanText,
		OnConflict: func(d time.Duration) {
			notify(ctx, "poll-conflict",
				fmt.Sprintf("getUpdates 409 selama %s: proses lain masih memegang bot; antrean tidak dibuang", d.Round(time.Second)))
		},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("gateway telegram: %w", err)
	}
	return adapter, limiter, nil
}

// startupRecovery closes cron slots orphaned by a previous process (REL-09).
func (g *serveGateway) startupRecovery(ctx context.Context, db *ctxdb.Store, stderr io.Writer) error {
	n, err := recoverCrashedRuns(ctx, db, g.notifyOwner)
	if err != nil {
		return err
	}
	if n > 0 {
		sessionsPrintf(stderr, "gateway: closed %d crashed cron slots\n", n)
	}
	return nil
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
	g.cancel, g.pollCancel, g.runDone = cancel, pollCancel, runCtx.Done()
	if err := g.registry.Start(pollCtx); err != nil {
		return fmt.Errorf("gateway: registry start: %w", err)
	}
	g.writePlatforms()
	g.wg.Add(6)
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
				g.dispatch(runCtx, &update)
			}
		}
	}()
	go func() { defer g.wg.Done(); g.deliveries(runCtx) }()
	go func() { defer g.wg.Done(); g.sendControlLoop(runCtx) }()
	go func() { defer g.wg.Done(); g.platformControlLoop(runCtx) }()
	go func() { defer g.wg.Done(); g.watchdog.Run(runCtx) }()
	go func() { defer g.wg.Done(); g.emergency(runCtx) }()
	g.resumeInbound(runCtx)
	return nil
}

func (g *serveGateway) Stop(ctx context.Context) {
	if g == nil {
		return
	}
	if g.watchdog != nil {
		g.watchdog.Disarm()
	}
	if g.cancel != nil {
		g.cancel()
	}
	g.chats.Close()
	if g.registry != nil {
		if err := g.registry.Stop(ctx); err != nil {
			sessionsPrintf(g.stderr, "gateway: stop adapters: %s\n", cleanLine(err.Error()))
		}
	}
	g.wg.Wait()
}

func (g *serveGateway) handle(ctx context.Context, in *telegram.Inbound, recorded bool) {
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
		UpdateID: in.UpdateID, Group: strings.HasPrefix(in.ChatID, "-"), Text: in.Text,
		Payload: `{"kind":"text","text":` + strconv.Quote(in.Text) + `}`,
	}
	work := func(wctx context.Context, t gateway.Ticket) error {
		turnCtx, done := g.beginTurn(wctx, &t.Scope)
		defer done()
		return g.run(turnCtx, in, &t)
	}
	admit := g.gate.Admit
	if recorded {
		admit = g.gate.AdmitRecorded
	}
	out, err := admit(ctx, inbound, work)
	switch {
	case err != nil:
		sessionsPrintf(g.stderr, "gateway: update %s: %s\n", cleanArg(in.UpdateID), cleanLine(err.Error()))
		g.metrics.IncError()
	case out.Kind == gateway.CommandHandled:
		g.replyCommand(ctx, in, out.Reply)
	case out.Kind == gateway.Rejected:
		g.reject(ctx, in, &out)
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
	sid, err := g.sessionFor(ctx, &t.Scope)
	if err == nil {
		g.beginProviderWait(in.UpdateID)
		err = g.runChat(ctx, in, sid, func(ctx context.Context, ch <-chan event.Update) error {
			return g.streamReply(ctx, in, ch, typing, ack)
		}, false)
		g.endProviderWait(in.UpdateID)
	}
	if err != nil {
		g.failPlaceholder(ctx, in, ack)
	}
	g.metrics.Turn(time.Since(start), err == nil)
	return err
}

func (g *serveGateway) sessionFor(ctx context.Context, scope *gateway.Scope) (string, error) {
	route := gwstore.Route{Platform: scope.Platform, ChatID: scope.ChatID, ThreadID: scope.ThreadID}
	principal := scope.Label()
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
	cwd := scope.Workspace
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

func (g *serveGateway) runChat(ctx context.Context, in *telegram.Inbound, sid string, deliver func(context.Context, <-chan event.Update) error, local bool) error {
	o := runOptions{
		prompt: in.Text, model: g.modelRef, approval: "ask", configPath: g.configPath, dataDir: g.dataDir,
		baseURL: strings.TrimSpace(os.Getenv("DAMNBOT_SERVE_BASE_URL")), apiKey: strings.TrimSpace(os.Getenv("DAMNBOT_SERVE_API_KEY")),
		session: sid, approvalOrigin: "run", cfgOverride: &g.cfg,
		gatewayIdentity: in.Platform + ":" + in.SenderID,
	}
	if tz, terr := g.Timezone(ctx, in.Platform, in.SenderID); terr == nil && tz != "" {
		if loc, lerr := time.LoadLocation(tz); lerr == nil {
			o.loc = loc
		}
	}
	if !local {
		o.approver = gatewayLoopApprover{g: g, chat: in.ChatID, thread: in.ThreadID}
		o.toolApprover = gatewayToolApprover{g: g, chat: in.ChatID, thread: in.ThreadID}
	}
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
	return deliver(ctx, ch)
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

func (g *serveGateway) replyHooks(ctx context.Context, chat string, now func() time.Time, start time.Time) (prefix string, human bool, decorate func(string, replyOutcome) string) {
	footer := g.chatFlag(ctx, chat, chatFooter)
	timestamps := g.chatFlag(ctx, chat, chatTimestamps)
	human = g.chatFlag(ctx, chat, chatHumanDelay)
	if timestamps {
		prefix = timestampPrefix(now())
	}
	decorate = func(text string, res replyOutcome) string {
		if !footer {
			return text
		}
		return text + footerText(g.modelRef, res.Meta, now().Sub(start))
	}
	return prefix, human, decorate
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
	now := g.now
	if now == nil {
		now = time.Now
	}
	prefix, human, decorate := g.replyHooks(ctx, in.ChatID, now, now())
	flush := func(ctx context.Context, text string) error {
		if err := g.humanPace(ctx, human); err != nil {
			return err
		}
		if err := flushTyped(ctx, reply, typing, withTimestamp(text, prefix)); err != nil {
			return err
		}
		if ack != nil {
			ack.replaced = true
		}
		return nil
	}
	res := streamReplyUpdates(ctx, ch, now, waitReply, flush, decorate, now(), shown,
		func(event.Update) { g.endProviderWait(in.UpdateID) })
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
