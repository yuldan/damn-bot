package gateway

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// CommandOptions configures a [Commands] dispatcher.
type CommandOptions struct {
	// BotUsername is the platform bot username used to match "/cmd@bot".
	// Empty accepts a mention of any bot; a non-empty value ignores (and so
	// leaves as text) a command addressed to another bot.
	BotUsername string
	// Approvals is the registry that "/approve" and "/deny" resolve. Nil
	// leaves both commands registered but unsupported.
	Approvals *Approvals
	// Resolver unblocks the transport waiting on the approval id.
	Resolver ApprovalResolver
	// Pairing redeems "/pair <code>"; nil leaves it registered but unsupported.
	Pairing PairingStore
	// Runtime backs "/stop", "/new", "/status" and "/usage"; nil leaves the
	// four registered but unsupported.
	Runtime Runtime
	// Control backs "/restart", "/platforms" and "/platform"; nil leaves them
	// registered but unsupported.
	Control  Control
	Settings ChatSettings
	Estop    *Estop
	// MCP backs "/reload-mcp"; nil leaves it registered but unsupported.
	MCP MCPReloader
	// Now is the pairing attempt clock and redemption timestamp; nil is time.Now.
	Now func() time.Time
}

// CommandResult is the answer to one message the admission layer extracted
// before building a turn. Handled means the message was a command for this
// bot: Reply (possibly empty, e.g. "/start") is the only output and no turn,
// LLM request, or tool call may follow.
type CommandResult struct {
	Handled bool
	// Applied is true exactly when this call resolved an approval (a second
	// identical "/approve" therefore never applies anything again).
	Applied bool
	Reply   string
}

// Commands dispatches the chat commands of SPEC §9.7 and the F4 gateway rows
// of docs/FEATURE-PARITY.md. Every registered command is either implemented or
// answers a clear "not supported" — there is no handler that pretends to work.
// Dispatch runs no tool and no shell command, and the parser it uses cannot;
// its side effects are exactly the approval decision, the pairing redemption,
// the /new session binding, and the /stop cancellation the caller asked for.
// A command never starts a turn.
type Commands struct {
	bot       string
	approvals *Approvals
	resolver  ApprovalResolver
	pairing   PairingStore
	runtime   Runtime
	settings  ChatSettings
	control   Control
	estop     *Estop
	mcp       MCPReloader
	now       func() time.Time
	specs     map[string]commandSpec

	mu           sync.Mutex
	pairAttempts map[string][]time.Time
}

// NewCommands returns a dispatcher over the command set.
func NewCommands(opts *CommandOptions) *Commands {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Commands{
		bot: strings.TrimPrefix(opts.BotUsername, "@"), approvals: opts.Approvals, resolver: opts.Resolver, pairing: opts.Pairing,
		runtime: opts.Runtime, settings: opts.Settings, control: opts.Control, estop: opts.Estop, mcp: opts.MCP,
		now: now, specs: commandRegistry(), pairAttempts: make(map[string][]time.Time),
	}
}

// commandSpec is one registry row. owner commands are refused for every role
// but owner before the implementation (or lack of one) is consulted, so a
// denial can never be confused with "unsupported". A nil run is a command this
// slice has not implemented.
type commandSpec struct {
	owner bool
	run   func(c *Commands, ctx context.Context, t *Ticket, cmd Command) CommandResult
}

// commandHelp is the one-line help an unknown command gets (SPEC §9.7: a
// command message never reaches the LLM).
const commandHelp = "commands: /start /whoami /approve [id] /deny [id|all] [reason] /pair <code> /new /stop /status /usage /pause [reason|off] /sethome /footer [on|off|status] /timestamps [on|off|status] /delay [on|off|status] /topic [off|help|session-id] /tz [zone|off] /restart /platforms /platform list|pause|resume [name] /reload-mcp"

// commandRegistry is the closed command set of this slice. Sources:
//   - SPEC §9.7 "Perintah chat": /new /stop /fork /model /usage /plan /undo
//     /approve /deny /resend /lock, and /pair.<code>;
//   - docs/FEATURE-PARITY.md F4 SLASH rows: /start /topic /pause /approve
//     /deny /whoami /sethome /footer /cron /restart /platforms /platform;
//   - docs/SELF-HEALING.md SAFE MODE: owner-only /status /logs /doctor
//     /resume.
//
// Only what has an implementation behind it runs. Tools and configuration are
// owner-only (SPEC §9.7), so their commands are too: /model, /cron, /pause,
// /restart, /sethome, /topic, /footer, /platform, /platforms, /lock, /resend.
// /status, /logs, /doctor and /resume are the owner commands SELF-HEALING
// names for SAFE MODE, and /usage reads the machine-wide ledger, so it is
// owner-only too. Session commands a user may need (/new, /stop) stay
// non-owner and act on the caller's own session only; /fork, /plan and /undo
// remain unsupported until their own slices implement them. /pair is
// non-owner and is also the one command the admission layer answers before an
// identity is allowlisted.
func commandRegistry() map[string]commandSpec {
	return map[string]commandSpec{
		"start":  {run: func(*Commands, context.Context, *Ticket, Command) CommandResult { return CommandResult{Handled: true} }},
		"whoami": {run: (*Commands).whoami},
		"approve": {
			owner: true,
			run:   (*Commands).approve,
		},
		"deny": {
			owner: true,
			run:   (*Commands).deny,
		},
		"pair": {run: (*Commands).pair},

		"new":  {run: (*Commands).newSession},
		"stop": {run: (*Commands).stop},
		"status": {
			owner: true,
			run:   (*Commands).status,
		},
		"tz": {run: (*Commands).timezone},
		"usage": {
			owner: true,
			run:   (*Commands).usage,
		},
		"restart":    {owner: true, run: (*Commands).restart},
		"platforms":  {owner: true, run: (*Commands).platformList},
		"gateway":    {owner: true, run: (*Commands).platformList},
		"platform":   {owner: true, run: (*Commands).platform},
		"pause":      {owner: true, run: (*Commands).pause},
		"cron":       {owner: true, run: (*Commands).cronCommand},
		"reload-mcp": {owner: true, run: (*Commands).reloadMCP},

		"sethome":    {owner: true, run: (*Commands).sethome},
		"topic":      {owner: true, run: (*Commands).topic},
		"footer":     {owner: true, run: (*Commands).footer},
		"timestamps": {owner: true, run: (*Commands).toggle},
		"delay":      {owner: true, run: (*Commands).toggle},

		// Owner-only, not implemented by this slice.
		"logs": {owner: true}, "doctor": {owner: true}, "resume": {owner: true},
		"lock": {owner: true}, "resend": {owner: true},
		"model": {owner: true},

		// Non-owner, not implemented by this slice.
		"fork": {}, "plan": {}, "undo": {},
	}
}

// reloadMCP re-reads [modules.mcp] and drops live clients, so a config edit
// takes effect without restarting the daemon. Only the owner may call it.
func (c *Commands) reloadMCP(ctx context.Context, _ *Ticket, _ Command) CommandResult {
	if c.mcp == nil {
		return CommandResult{Handled: true, Reply: "command /reload-mcp is not supported in this build"}
	}
	n, err := c.mcp.ReloadMCP(ctx)
	if err != nil {
		return CommandResult{Handled: true, Reply: "mcp reload failed; previous servers stay active"}
	}
	return CommandResult{Handled: true, Reply: fmt.Sprintf("mcp: %d server(s) reloaded", n)}
}

// Dispatch classifies text and, when it is a command for this bot, answers it.
// A message that is not a command (including "/etc/hosts": the command word may
// not contain '/') is left alone with Handled=false and the caller treats it as
// text. An unknown but well-formed command is answered here, so it never
// reaches a turn.
func (c *Commands) Dispatch(ctx context.Context, t *Ticket, text string) CommandResult {
	cmd, ok := c.addresses(text)
	if !ok {
		return CommandResult{}
	}
	spec, known := c.specs[cmd.Name]
	if !known {
		return CommandResult{Handled: true, Reply: fmt.Sprintf("unknown command /%s. %s", cmd.Name, commandHelp)}
	}
	if spec.owner && t.Identity.Role != RoleOwner {
		return CommandResult{Handled: true, Reply: fmt.Sprintf("command /%s is owner-only", cmd.Name)}
	}
	if spec.run == nil {
		return CommandResult{Handled: true, Reply: fmt.Sprintf("command /%s is not supported in this build", cmd.Name)}
	}
	return spec.run(c, ctx, t, cmd)
}

// addresses parses text and reports whether it is a command for this bot. A
// mention of another bot is deliberately left as text.
func (c *Commands) addresses(text string) (Command, bool) {
	cmd, ok := Parse(text)
	if !ok {
		return Command{}, false
	}
	if cmd.Mention != "" && c.bot != "" && !strings.EqualFold(cmd.Mention, c.bot) {
		return Command{}, false
	}
	return cmd, true
}

func (c *Commands) emergency(text string) bool {
	if c == nil {
		return false
	}
	cmd, ok := c.addresses(text)
	if !ok {
		return false
	}
	return cmd.Name == "pause" || cmd.Name == "status"
}

// pairCommand is the pre-authorization view of Dispatch: only "/pair" is
// recognized, because pairing is the one command a not-yet-allowlisted
// identity may send (SPEC §9.7). Every other message still goes through
// default-deny authorization.
func (c *Commands) pairCommand(text string) (Command, bool) {
	if c.pairing == nil { // no seam: leave the message to default deny
		return Command{}, false
	}
	cmd, ok := c.addresses(text)
	if !ok || cmd.Name != "pair" {
		return Command{}, false
	}
	return cmd, true
}

// whoami implements SLASH-041: the caller's slash access (admin / user).
func (c *Commands) whoami(_ context.Context, t *Ticket, _ Command) CommandResult {
	if t.Identity.Role == RoleOwner {
		return CommandResult{Handled: true, Reply: "access: admin (owner)"}
	}
	return CommandResult{Handled: true, Reply: "access: user"}
}

// stop implements SPEC §9.7 "/stop": cancel the caller's active turn in this
// chat through the runner's existing context seam. The cancel is scoped to the
// caller's session, so another chat (or another principal's session in a group)
// keeps running. A cancelled turn keeps its slot until its runner returns, so a
// second /stop reports that nothing is active instead of cancelling again.
func (c *Commands) stop(ctx context.Context, t *Ticket, _ Command) CommandResult {
	if c.runtime == nil {
		return unsupported("stop")
	}
	cancelled, err := c.runtime.StopTurn(ctx, &t.Scope)
	switch {
	case err != nil:
		return CommandResult{Handled: true, Reply: "could not stop the turn"}
	case cancelled:
		return CommandResult{Handled: true, Reply: "turn cancelled"}
	default:
		return CommandResult{Handled: true, Reply: "no active turn in this chat"}
	}
}

// newSession implements SPEC §9.7 "/new": bind a fresh session to the caller's
// route. The previous session stays stored and readable; nothing is deleted.
func (c *Commands) newSession(ctx context.Context, t *Ticket, _ Command) CommandResult {
	if c.runtime == nil {
		return unsupported("new")
	}
	if _, err := c.runtime.NewSession(ctx, &t.Scope); err != nil {
		return CommandResult{Handled: true, Reply: "could not start a new session"}
	}
	return CommandResult{Handled: true, Reply: "started a new session; the previous one is kept"}
}

func (c *Commands) sethome(ctx context.Context, t *Ticket, _ Command) CommandResult {
	if c.settings == nil {
		return unsupported("sethome")
	}
	p, cid, tid := t.Scope.Platform, t.Scope.ChatID, t.Scope.ThreadID
	curP, curC, curT, found, err := c.settings.HomeChannel(ctx)
	if err != nil {
		return CommandResult{Handled: true, Reply: "home channel unavailable"}
	}
	if found && curP == p && curC == cid && curT == tid {
		return CommandResult{Handled: true, Reply: "home channel already " + HomeName(p, cid, tid)}
	}
	if err := c.settings.SetHomeChannel(ctx, p, cid, tid, c.now().UnixMilli()); err != nil {
		return CommandResult{Handled: true, Reply: "could not set home channel"}
	}
	return CommandResult{Handled: true, Reply: "home channel set to " + HomeName(p, cid, tid)}
}

func (c *Commands) footer(ctx context.Context, t *Ticket, cmd Command) CommandResult {
	if c.settings == nil {
		return unsupported("footer")
	}
	arg := "status"
	if len(cmd.Args) > 0 {
		arg = strings.ToLower(cmd.Args[0])
	}
	if len(cmd.Args) > 1 || (arg != "on" && arg != "off" && arg != "status") {
		return usage("footer [on|off|status]")
	}
	if arg == "status" {
		on, err := c.chatFlag(ctx, t, "footer")
		if err != nil {
			return CommandResult{Handled: true, Reply: "footer setting unavailable"}
		}
		if on {
			return CommandResult{Handled: true, Reply: "footer on"}
		}
		return CommandResult{Handled: true, Reply: "footer off (default)"}
	}
	if err := c.settings.SetChatSetting(ctx, t.Scope.Platform, t.Scope.ChatID, "footer", strconv.FormatBool(arg == "on"), c.now().UnixMilli()); err != nil {
		return CommandResult{Handled: true, Reply: "could not change footer"}
	}
	if arg == "on" {
		return CommandResult{Handled: true, Reply: "footer on"}
	}
	return CommandResult{Handled: true, Reply: "footer off"}
}

type topicControl interface {
	TopicSession(ctx context.Context, scope *Scope) (string, error)
	TopicOff(ctx context.Context, scope *Scope) error
	BindTopicSession(ctx context.Context, scope *Scope, sessionID string) error
}

func (c *Commands) topic(ctx context.Context, t *Ticket, cmd Command) CommandResult {
	if t.Scope.ThreadID == "" {
		return CommandResult{Handled: true, Reply: "no topic thread in this chat; /topic needs a forum topic"}
	}
	tc, ok := c.control.(topicControl)
	if !ok {
		tc, ok = c.runtime.(topicControl)
	}
	if !ok {
		return unsupported("topic")
	}
	arg := "help"
	if len(cmd.Args) > 0 {
		arg = cmd.Args[0]
	}
	switch arg {
	case "help":
		sid, err := tc.TopicSession(ctx, &t.Scope)
		if err != nil {
			return CommandResult{Handled: true, Reply: "topic session unavailable"}
		}
		return CommandResult{Handled: true, Reply: fmt.Sprintf("topic %s: session %s", t.Scope.ThreadID, sid)}
	case "off":
		if err := tc.TopicOff(ctx, &t.Scope); err != nil {
			return CommandResult{Handled: true, Reply: "could not turn topic sessions off"}
		}
		return CommandResult{Handled: true, Reply: "topic session off; this chat now uses the chat session"}
	default:
		if len(cmd.Args) != 1 {
			return usage("topic [off|help|session-id]")
		}
		if err := tc.BindTopicSession(ctx, &t.Scope, arg); err != nil {
			return CommandResult{Handled: true, Reply: fmt.Sprintf("session %q not found", arg)}
		}
		return CommandResult{Handled: true, Reply: fmt.Sprintf("topic %s bound to session %s", t.Scope.ThreadID, arg)}
	}
}

// status implements the owner-only "/status" of SELF-HEALING: uptime, queue
// depth, active turns, mode and version. It reads runtime state only.
func (c *Commands) status(ctx context.Context, t *Ticket, _ Command) CommandResult {
	if c.runtime == nil {
		return unsupported("status")
	}
	st, err := c.runtime.Status(ctx)
	if err != nil {
		return CommandResult{Handled: true, Reply: "status unavailable"}
	}
	version := st.Version
	if version == "" {
		version = "unknown"
	}
	home, stop := "", "off"
	if st.Home != "" {
		home = ", home " + st.Home
	}
	if st.Paused {
		stop = "on (" + st.PauseReason + ")"
	}
	waiting := ""
	if st.WaitingProvider >= ProviderWaitVisible {
		waiting = fmt.Sprintf(", menunggu provider %d s", int64(st.WaitingProvider/time.Second))
	}
	tzName := "host"
	if zones, ok := c.runtime.(TimeZones); ok && t != nil {
		if zone, zerr := zones.Timezone(ctx, t.Scope.Platform, t.Scope.UserID); zerr == nil && zone != "" {
			tzName = zone
		}
	}
	return CommandResult{Handled: true, Reply: fmt.Sprintf(
		"status: up %s, queue %d, active turns %d, emergency stop %s, mode %s, version %s%s%s, tz %s",
		st.Uptime.Truncate(time.Second), st.QueueDepth, st.ActiveTurns, stop, modeName(st.SafeMode), version, home, waiting, tzName)}
}

// TimeZones is the per-identity IANA zone of PROV-03. Implemented by the
// serve runtime over gateway_identities.tz.
type TimeZones interface {
	Timezone(ctx context.Context, platform, externalID string) (string, error)
	SetTimezone(ctx context.Context, platform, externalID, tz string) error
}

// timezone implements /tz [IANA|off]: every user may set their own zone.
func (c *Commands) timezone(ctx context.Context, t *Ticket, cmd Command) CommandResult {
	zones, ok := c.runtime.(TimeZones)
	if !ok || t == nil {
		return unsupported("tz")
	}
	current, err := zones.Timezone(ctx, t.Scope.Platform, t.Scope.UserID)
	if err != nil {
		return CommandResult{Handled: true, Reply: "tz unavailable"}
	}
	if len(cmd.Args) == 0 {
		return CommandResult{Handled: true, Reply: tzReply(current)}
	}
	zone := cmd.Args[0]
	if zone == "off" || zone == "host" || zone == "local" {
		zone = ""
	}
	if zone != "" {
		if _, lerr := time.LoadLocation(zone); lerr != nil {
			return CommandResult{Handled: true, Reply: "tz: unknown IANA zone " + zone}
		}
	}
	if err := zones.SetTimezone(ctx, t.Scope.Platform, t.Scope.UserID, zone); err != nil {
		return CommandResult{Handled: true, Reply: "tz: could not save"}
	}
	return CommandResult{Handled: true, Reply: tzReply(zone)}
}

func tzReply(zone string) string {
	if zone == "" {
		return "tz: host"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return "tz: " + zone
	}
	return "tz: " + zone + " (" + time.Now().In(loc).Format("2006-01-02 15:04 MST Mon") + ")"
}

// pause implements SLASH-021: "/pause [reason]" and "/pause off".
func (c *Commands) pause(ctx context.Context, _ *Ticket, cmd Command) CommandResult {
	if c.estop == nil {
		return unsupported("pause")
	}
	if len(cmd.Args) > 0 && strings.EqualFold(cmd.Args[0], "off") {
		before, berr := c.estop.Status(ctx)
		if berr != nil {
			before = EstopState{Paused: true}
		}
		if _, err := c.estop.Resume(ctx); err != nil {
			return CommandResult{Handled: true, Reply: "could not lift the emergency stop"}
		}
		if before.Paused {
			return CommandResult{Handled: true, Reply: "emergency stop off; parked messages and due cron slots resume"}
		}
		return CommandResult{Handled: true, Reply: "emergency stop is not on"}
	}
	reason := strings.TrimSpace(cmd.Rest)
	if reason == "" {
		reason = "owner /pause"
	}
	if r := []rune(reason); len(r) > 120 {
		reason = string(r[:120])
	}
	before, berr := c.estop.Status(ctx)
	if berr != nil {
		before = EstopState{Paused: true}
	}
	if _, err := c.estop.Pause(ctx, reason); err != nil {
		return CommandResult{Handled: true, Reply: "could not turn the emergency stop on"}
	}
	if before.Paused && !before.Since.IsZero() {
		return CommandResult{Handled: true, Reply: fmt.Sprintf(
			"emergency stop already on since %s (%s); /pause off resumes",
			before.Since.UTC().Format(time.RFC3339), before.Reason)}
	}
	return CommandResult{Handled: true, Reply: fmt.Sprintf(
		"emergency stop on: new turns and cron dispatch are paused (%s); /pause off resumes", reason)}
}

// usage implements the owner-only SPEC §9.7 "/usage": the token and cost totals
// of the caller's session and of today, read from the usage ledger. Without a
// ledger row it says so; an unknown price is never printed as zero.
func (c *Commands) usage(ctx context.Context, t *Ticket, _ Command) CommandResult {
	if c.runtime == nil {
		return unsupported("usage")
	}
	rep, ok, err := c.runtime.Usage(ctx, &t.Scope)
	switch {
	case err != nil:
		return CommandResult{Handled: true, Reply: "usage unavailable"}
	case !ok:
		return CommandResult{Handled: true, Reply: noUsage}
	}
	return CommandResult{Handled: true, Reply: fmt.Sprintf(
		"usage: in %d, out %d tokens; session cost %s; today cost %s",
		rep.SessionIn, rep.SessionOut, usd(rep.SessionCostKnown, rep.SessionCostMicros), usd(rep.DayCostKnown, rep.DayCostMicros))}
}

const noUsage = "no usage yet"

// restart is SLASH-089: stop admission, reply, then leave once active turns end.
func (c *Commands) restart(ctx context.Context, t *Ticket, _ Command) CommandResult {
	if c.control == nil {
		return unsupported("restart")
	}
	rep, err := c.control.Restart(ctx, &t.Scope)
	switch {
	case err != nil:
		return CommandResult{Handled: true, Reply: "restart unavailable"}
	case rep.Active > 0:
		return CommandResult{Handled: true, Reply: fmt.Sprintf("restart scheduled after %d active turn(s) finish", rep.Active)}
	default:
		return CommandResult{Handled: true, Reply: "restarting"}
	}
}

// platform is SLASH-096: pause, resume or list one adapter; both are idempotent.
func (c *Commands) platform(ctx context.Context, _ *Ticket, cmd Command) CommandResult {
	if c.control == nil {
		return unsupported("platform")
	}
	if len(cmd.Args) == 0 || cmd.Args[0] == "list" {
		return c.platformList(ctx, nil, Command{})
	}
	verb := cmd.Args[0]
	if (verb != "pause" && verb != "resume") || len(cmd.Args) != 2 {
		return usage("/platform pause|resume|list [name]")
	}
	var (
		st  AdapterStatus
		err error
	)
	if verb == "pause" {
		st, err = c.control.PausePlatform(ctx, cmd.Args[1])
	} else {
		st, err = c.control.ResumePlatform(ctx, cmd.Args[1])
	}
	if err != nil {
		if errors.Is(err, ErrAdapterNotFound) {
			return CommandResult{Handled: true, Reply: fmt.Sprintf("platform %q not found", cmd.Args[1])}
		}
		return CommandResult{Handled: true, Reply: "could not " + verb + " platform " + cmd.Args[1]}
	}
	return CommandResult{Handled: true, Reply: fmt.Sprintf("platform %s %s", st.Key, st.State)}
}

func (c *Commands) platformList(ctx context.Context, _ *Ticket, _ Command) CommandResult {
	if c.control == nil {
		return unsupported("platforms")
	}
	st, err := c.control.Platforms(ctx)
	if err != nil {
		return CommandResult{Handled: true, Reply: "platform status unavailable"}
	}
	if len(st) == 0 {
		return CommandResult{Handled: true, Reply: "no gateway platforms"}
	}
	rows := make([]string, len(st))
	for i, p := range st {
		rows[i] = p.Key.String() + " " + string(p.State)
		if p.Err != "" {
			rows[i] += " (" + p.Err + ")"
		}
	}
	return CommandResult{Handled: true, Reply: "platforms: " + strings.Join(rows, ", ")}
}

// modeName is the /status name of the daemon mode (SELF-HEALING §1.4).
func modeName(safe bool) string {
	if safe {
		return "safe"
	}
	return "normal"
}

// usd renders micro-USD, or "unknown" when the ledger counted a call without a
// price: an unknown cost is never presented as a zero.
func usd(known bool, micros int64) string {
	if !known {
		return "unknown"
	}
	return "$" + strconv.FormatFloat(float64(micros)/1e6, 'f', 6, 64)
}

// approve implements SPEC §9.7 "/approve <id>". Bare "/approve" targets the one
// open approval of this chat; several open approvals are ambiguous and change
// nothing. Only an open approval of this chat can be resolved, and the decision
// is applied at most once (the second try reports the existing answer).
func (c *Commands) approve(ctx context.Context, t *Ticket, cmd Command) CommandResult {
	if c.approvals == nil {
		return unsupported("approve")
	}
	if len(cmd.Args) > 1 {
		return usage("/approve [id]")
	}
	id, res, ok := c.target(t, cmd.Args)
	if !ok {
		return res
	}
	applied, reply := c.decide(t, id, AllowOnce, "approved")
	if applied && c.resolver != nil {
		if ok, err := c.resolver.ResolveApproval(ctx, &t.Scope, id, true); err != nil || !ok {
			return CommandResult{Handled: true, Reply: "approval " + id + " is no longer pending"}
		}
	}
	return CommandResult{Handled: true, Applied: applied, Reply: reply}
}

// deny implements SPEC §9.7 "/deny <id> [reason]" and the parity form
// "/deny all [reason]". The reason is accepted for compatibility but not stored:
// the approval seam carries no reason field.
func (c *Commands) deny(ctx context.Context, t *Ticket, cmd Command) CommandResult {
	if c.approvals == nil {
		return unsupported("deny")
	}
	if len(cmd.Args) > 0 && strings.EqualFold(cmd.Args[0], "all") {
		return c.denyAll(t)
	}
	id, res, ok := c.target(t, cmd.Args)
	if !ok {
		return res
	}
	applied, reply := c.decide(t, id, Deny, "denied")
	if applied && c.resolver != nil {
		if ok, err := c.resolver.ResolveApproval(ctx, &t.Scope, id, false); err != nil || !ok {
			return CommandResult{Handled: true, Reply: "approval " + id + " is no longer pending"}
		}
	}
	return CommandResult{Handled: true, Applied: applied, Reply: reply}
}

// denyAll denies every open approval of this chat, each exactly once.
func (c *Commands) denyAll(t *Ticket) CommandResult {
	if c.approvals == nil {
		return unsupported("deny")
	}
	applied := 0
	for _, p := range c.approvals.Pending(t.Scope.Platform, t.Scope.ChatID) {
		if ok, err := c.approvals.Decide(t.Scope.Platform, t.Scope.ChatID, p.ID, Deny); err == nil && ok {
			applied++
		}
	}
	if applied == 0 {
		return CommandResult{Handled: true, Reply: noOpenApproval}
	}
	return CommandResult{Handled: true, Applied: true, Reply: fmt.Sprintf("denied %d approval(s)", applied)}
}

// target resolves an explicit id or the single open approval of the chat. ok
// false carries the reply that reports the ambiguity or the empty set.
func (c *Commands) target(t *Ticket, args []string) (string, CommandResult, bool) {
	if len(args) > 0 {
		return args[0], CommandResult{}, true
	}
	pending := c.approvals.Pending(t.Scope.Platform, t.Scope.ChatID)
	switch len(pending) {
	case 0:
		return "", CommandResult{Handled: true, Reply: noOpenApproval}, false
	case 1:
		return pending[0].ID, CommandResult{}, true
	}
	ids := make([]string, len(pending))
	for i := range pending {
		ids[i] = fmt.Sprintf("%q", pending[i].ID)
	}
	return "", CommandResult{
		Handled: true,
		Reply:   fmt.Sprintf("multiple open approvals in this chat; use an id: %s", strings.Join(ids, ", ")),
	}, false
}

// decide resolves id and translates the registry's closed errors into one
// reply. applied is true only for the call that changed the decision.
func (c *Commands) decide(t *Ticket, id string, d Decision, verb string) (applied bool, reply string) {
	applied, err := c.approvals.Decide(t.Scope.Platform, t.Scope.ChatID, id, d)
	switch {
	case err == nil:
	case errors.Is(err, ErrApprovalNotFound):
		return false, fmt.Sprintf("approval %q not found in this chat", id)
	case errors.Is(err, ErrApprovalConflict):
		return false, fmt.Sprintf("approval %q was already decided differently", id)
	default:
		return false, fmt.Sprintf("approval %q could not be resolved", id)
	}
	if applied {
		return true, fmt.Sprintf("%s %q", verb, id)
	}
	if decision, ok := c.approvals.Decision(id); ok && decision == d {
		return false, fmt.Sprintf("approval %q is already %s", id, verb)
	}
	return false, fmt.Sprintf("approval %q is no longer open", id)
}

const noOpenApproval = "no open approval in this chat"

func unsupported(name string) CommandResult {
	return CommandResult{Handled: true, Reply: fmt.Sprintf("command /%s is not supported in this build", name)}
}

func usage(u string) CommandResult {
	return CommandResult{Handled: true, Reply: "usage: /" + strings.TrimPrefix(u, "/")}
}
