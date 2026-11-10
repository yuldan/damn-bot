package gateway

import "context"

// F8Commands is the seam behind the F8 slash commands (SLASH-077/078/030/
// 031/034/037/049, and the later surfaces). The gateway owns dispatch and the
// chat render cap; the implementation owns parsing and owner checks.
type F8Commands interface {
	// F8Gateway answers one F8 command. name is the command as typed
	// (aliases included); the implementation canonicalises it.
	F8Gateway(ctx context.Context, role Role, sessionID, name string, args []string) string
}

// f8Command routes one F8 slash command to the seam. A nil seam is a typed
// module_unavailable answer, never a silent turn.
func (c *Commands) f8Command(ctx context.Context, t *Ticket, cmd Command) CommandResult {
	if c.f8 == nil {
		return CommandResult{Handled: true, Reply: "module_unavailable: /" + cmd.Name + " is not wired in this build"}
	}
	reply := c.f8.F8Gateway(ctx, t.Identity.Role, t.Scope.Label(), cmd.Name, cmd.Args)
	return CommandResult{Handled: true, Reply: limitChatReply(reply)}
}

// addF8Specs registers the F8 slash names in the command registry. Keeping
// them here leaves commands.go below the 800-line file cap (E-14).
func addF8Specs(m map[string]commandSpec) {
	m["suggestions"] = commandSpec{run: (*Commands).f8Command}
	m["suggest"] = commandSpec{run: (*Commands).f8Command}
	m["blueprint"] = commandSpec{run: (*Commands).f8Command}
	m["bp"] = commandSpec{run: (*Commands).f8Command}
	m["goal"] = commandSpec{run: (*Commands).f8Command}
	m["journey"] = commandSpec{run: (*Commands).f8Command}
	m["learning"] = commandSpec{run: (*Commands).f8Command}
	m["memory-graph"] = commandSpec{run: (*Commands).f8Command}
	m["pet"] = commandSpec{run: (*Commands).f8Command}
}
