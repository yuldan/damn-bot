package gateway

import (
	"errors"
	"fmt"
	"strings"
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
// Dispatch is read-only apart from the approval decisions it is asked to make;
// it never runs a tool or a shell command, and the parser it uses cannot.
type Commands struct {
	bot       string
	approvals *Approvals
	specs     map[string]commandSpec
}

// NewCommands returns a dispatcher over the command set. It has no I/O and
// needs no validation: the options are data, and a missing approval registry
// only narrows what "/approve" and "/deny" can do.
func NewCommands(opts CommandOptions) *Commands {
	return &Commands{
		bot:       strings.TrimPrefix(opts.BotUsername, "@"),
		approvals: opts.Approvals,
		specs:     commandRegistry(),
	}
}

// commandSpec is one registry row. owner commands are refused for every role
// but owner before the implementation (or lack of one) is consulted, so a
// denial can never be confused with "unsupported". A nil run is a command this
// slice has not implemented.
type commandSpec struct {
	owner bool
	run   func(c *Commands, t *Ticket, cmd Command) CommandResult
}

// commandHelp is the one-line help an unknown command gets (SPEC §9.7: a
// command message never reaches the LLM).
const commandHelp = "commands: /start /whoami /approve [id] /deny [id|all] [reason]"

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
// names for SAFE MODE. Session commands a user may need (/new, /stop, /fork,
// /usage, /plan, /undo) and /pair stay non-owner; they are unsupported until
// their own slices implement them.
func commandRegistry() map[string]commandSpec {
	return map[string]commandSpec{
		"start":  {run: func(*Commands, *Ticket, Command) CommandResult { return CommandResult{Handled: true} }},
		"whoami": {run: (*Commands).whoami},
		"approve": {
			owner: true,
			run:   (*Commands).approve,
		},
		"deny": {
			owner: true,
			run:   (*Commands).deny,
		},

		// Owner-only, not implemented by this slice.
		"status": {owner: true}, "logs": {owner: true}, "doctor": {owner: true}, "resume": {owner: true},
		"lock": {owner: true}, "restart": {owner: true}, "pause": {owner: true}, "resend": {owner: true},
		"model": {owner: true}, "sethome": {owner: true}, "topic": {owner: true}, "footer": {owner: true},
		"platform": {owner: true}, "platforms": {owner: true}, "cron": {owner: true},

		// Non-owner, not implemented by this slice.
		"new": {}, "stop": {}, "fork": {}, "usage": {}, "plan": {}, "undo": {},
		"pair": {},
	}
}

// Dispatch classifies text and, when it is a command for this bot, answers it.
// A message that is not a command (including "/etc/hosts": the command word may
// not contain '/') is left alone with Handled=false and the caller treats it as
// text. An unknown but well-formed command is answered here, so it never
// reaches a turn.
func (c *Commands) Dispatch(t *Ticket, text string) CommandResult {
	cmd, ok := Parse(text)
	if !ok {
		return CommandResult{}
	}
	if cmd.Mention != "" && c.bot != "" && !strings.EqualFold(cmd.Mention, c.bot) {
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
	return spec.run(c, t, cmd)
}

// whoami implements SLASH-041: the caller's slash access (admin / user).
func (c *Commands) whoami(t *Ticket, _ Command) CommandResult {
	if t.Identity.Role == RoleOwner {
		return CommandResult{Handled: true, Reply: "access: admin (owner)"}
	}
	return CommandResult{Handled: true, Reply: "access: user"}
}

// approve implements SPEC §9.7 "/approve <id>". Bare "/approve" targets the one
// open approval of this chat; several open approvals are ambiguous and change
// nothing. Only an open approval of this chat can be resolved, and the decision
// is applied at most once (the second try reports the existing answer).
func (c *Commands) approve(t *Ticket, cmd Command) CommandResult {
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
	return CommandResult{Handled: true, Applied: applied, Reply: reply}
}

// deny implements SPEC §9.7 "/deny <id> [reason]" and the parity form
// "/deny all [reason]". The reason is accepted for compatibility but not stored:
// the approval seam carries no reason field.
func (c *Commands) deny(t *Ticket, cmd Command) CommandResult {
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
	return CommandResult{Handled: true, Reply: "usage: /" + u}
}
