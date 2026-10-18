package classify

// Mode is the approval mode of the call (SPEC §14.1).
type Mode uint8

// Modes; the headless ones have no interactive approver.
const (
	ModeAsk Mode = iota
	ModeAutoEdit
	ModeAuto
	ModeHeadless
	ModeUnsafeAutoShell // headless --approval auto --unsafe-auto-shell
)

// Context carries the call facts Decide needs beyond the Verdict. The
// zero value is the most restrictive except TurnTainted and
// WroteWithoutApproval, which callers must set from session state.
type Context struct {
	Mode                 Mode
	AllowMatched         bool // MatchAllow result
	WorkspaceTrusted     bool
	TurnTainted          bool
	Session              bool // persistent session call: never shell_allow
	Stdin                bool // stdin parameter set: never shell_allow
	CwdInRoot            bool
	WroteWithoutApproval bool // file written without approval since the last manual shell approval
	PlanMode             bool
	Verify               bool // runtime verify: headless refusal is Unverified
}

// Decision is the approval outcome for one shell call.
type Decision uint8

// Decisions. Only AutoApprove runs without a human.
const (
	Ask           Decision = iota
	AskPreview             // preview with labels/flags (reads outside workspace)
	AskPerCommand          // privileged: per command, full argv preview
	AutoApprove
	Denied
	DeniedPathError  // denied_path: not approvable in any mode
	OutsideWorkspace // headless outside_read
	Unverified       // headless verify not run (evidence_gap)
)

// String returns the decision name used in tests and audit.
func (d Decision) String() string {
	return [...]string{"ask", "ask_preview", "ask_per_command", "auto", "denied", "denied_path", "outside_workspace", "unverified"}[d]
}

// Decide applies the shell rows of SPEC §14.1 (ADR-079 butir 3–4 and 11,
// ADR-088). Classification never auto-approves: AutoApprove comes only
// from an exact shell_allow match with every condition, or from
// --unsafe-auto-shell for a plain mutating command.
func Decide(v *Verdict, c *Context) Decision {
	if v.Flags&DeniedPath != 0 {
		return DeniedPathError
	}
	allowed := allowAuto(v, c)
	switch {
	case c.PlanMode && allowed && v.NonMutating:
		return AutoApprove
	case c.PlanMode:
		return Denied
	case allowed:
		return AutoApprove
	case c.Mode >= ModeHeadless:
		return headless(v, c)
	case v.Class != Mutating:
		return AskPreview
	case v.Flags&Privileged != 0:
		return AskPerCommand
	case v.Flags&OutsideRead != 0:
		return AskPreview
	}
	return Ask
}

// allowAuto is the shell_allow path: an exact match plus conditions 1–5
// of EXEC-AND-CODING §A.8.
func allowAuto(v *Verdict, c *Context) bool {
	return c.AllowMatched && !c.Session && !c.Stdin && c.CwdInRoot && c.WorkspaceTrusted && !c.TurnTainted &&
		!c.WroteWithoutApproval && v.Class == Mutating && v.Flags&(Privileged|OutsideRead|ReferencesSecret) == 0
}

// headless: outside_read → outside_workspace; plain mutating may run only
// under --unsafe-auto-shell in a trusted, untainted workspace.
func headless(v *Verdict, c *Context) Decision {
	switch {
	case v.Class != Mutating || v.Flags&Privileged != 0:
	case v.Flags&OutsideRead != 0:
		return OutsideWorkspace
	case c.Mode == ModeUnsafeAutoShell && c.WorkspaceTrusted && !c.TurnTainted && c.CwdInRoot:
		return AutoApprove
	}
	if c.Verify {
		return Unverified
	}
	return Denied
}

// OutputSource returns the <untrusted source="…"> wrapper for a command's
// output, "" when none; any non-empty source taints the turn (ADR-070,
// ADR-079 butir 6, ADR-088 butir 2).
func OutputSource(v *Verdict, workspaceTrusted bool) string {
	switch {
	case !workspaceTrusted:
		return "workspace"
	case v.Flags&OutsideRead != 0:
		return "shell:outside_workspace"
	case v.Flags&Network != 0:
		return "shell:network"
	}
	return ""
}
