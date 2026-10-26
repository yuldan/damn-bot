package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Mode is the session approval mode (SPEC §14.1). ModeHeadless is a
// non-interactive run: no approver is consulted and writes are denied.
// ModeHeadlessAuto is `run --approval auto`, which auto-approves workspace
// writes but never shell beyond shell_allow/--unsafe-auto-shell.
type Mode uint8

// Approval modes in increasing automation.
const (
	ModeAsk Mode = iota
	ModeAutoEdit
	ModeAuto
	ModeHeadless
	ModeHeadlessAuto
)

// Headless reports whether no interactive approver is available.
func (m Mode) Headless() bool { return m == ModeHeadless || m == ModeHeadlessAuto }

// PolicyContext is the state the pipeline supplies to a tool's Inspector.
type PolicyContext struct {
	Mode             Mode
	Tainted          bool
	WorkspaceTrusted bool
	Cwd              string
	// PlanMode restricts execution to reads and shell commands that match an
	// exact shell_allow entry and carry the classifier's non_mutating label
	// (SPEC §18.3, ADR-079 butir 11, ADR-203).
	PlanMode bool
}

// Approval origins (SPEC §18.2, ADR-202). The value names the surface that
// asked; an approval never crosses surfaces. OriginRun is the headless `run`
// transport of SPEC §18.2 and the default when a caller sets none.
const (
	OriginRun  = "run"
	OriginTUI  = "tui"
	OriginACP  = "acp"
	OriginPlan = "plan"
)

// ValidOrigin reports whether s is one of the reviewed origins.
func ValidOrigin(s string) bool {
	switch s {
	case OriginRun, OriginTUI, OriginACP, OriginPlan:
		return true
	}
	return false
}

// Requirement is the policy outcome before the approver is consulted.
type Requirement uint8

// Policy outcomes.
const (
	RequireAuto       Requirement = iota // run without asking
	RequireAsk                           // ask the interactive approver
	RequireDeny                          // denied in this mode
	RequireOutside                       // outside the workspace: ask, or deny when headless
	RequireDeniedPath                    // deny_paths: never approvable
)

// PolicyDecision is one tool's approval requirement for one call.
type PolicyDecision struct {
	Requirement  Requirement
	Paths        []string // exact subjects for allow_session and audit
	Preview      string   // model-facing preview (SPEC §18.2); may be empty
	Class        string   // audit label ("read", "write", "shell:destructive", …)
	AllowSession bool     // false for shell and P26 (ADR-088)
	// PlanAllowed is set only by a shell decision that matched an exact
	// shell_allow entry and was labelled non_mutating. The central plan gate
	// accepts a non-read effect only when this is true (ADR-203).
	PlanAllowed bool
}

// Inspector is implemented by tools whose policy depends on the arguments.
// Tools without it use defaultPolicy.
type Inspector interface {
	Inspect(ctx context.Context, env Env, args json.RawMessage, pc PolicyContext) (PolicyDecision, error)
}

// defaultPolicy is the code-tool policy for tools without an Inspector:
// reads are auto, writes follow the mode, headless writes are denied.
func defaultPolicy(effect Effect, pc PolicyContext) PolicyDecision {
	if effect == EffectRead {
		return PolicyDecision{Requirement: RequireAuto, Class: "read"}
	}
	if pc.PlanMode {
		return PolicyDecision{Requirement: RequireDeny, Class: "plan_mode"}
	}
	switch pc.Mode {
	case ModeAutoEdit, ModeAuto, ModeHeadlessAuto:
		return PolicyDecision{Requirement: RequireAuto, Class: "write"}
	case ModeHeadless:
		return PolicyDecision{Requirement: RequireDeny, Class: "write"}
	case ModeAsk:
		return PolicyDecision{Requirement: RequireAsk, Class: "write", AllowSession: true}
	}
	return PolicyDecision{Requirement: RequireDeny, Class: "write"}
}

// Approval is an interactive approver's answer. Edit (modify then retry) is
// not offered in F1; an unexpected value fails closed as deny.
type Approval uint8

// Approver answers.
const (
	ApprovalAllowOnce Approval = iota
	ApprovalAllowSession
	ApprovalDeny
)

// ApprovalRequest is one approval prompt (SPEC §14.1, §18.2).
type ApprovalRequest struct {
	Tool    string
	CallID  string
	Effect  Effect
	Class   string
	Paths   []string
	Preview string
	Origin  string // run|tui|acp|plan (ADR-202); never empty from the dispatcher
	Tainted bool
	// AllowSession is false for shell (ADR-088 butir 1), execute_code and
	// P26 classes (irreversible/monetary/delegated_agent_bypass).
	AllowSession bool
}

// MaxPreview caps one approval preview in runes (SPEC §18.2 channels render at
// most 3.500 characters; the truncation marker is added after the cap).
const MaxPreview = 3500

// clipPreview bounds a preview at a rune boundary and marks truncation
// explicitly so an approver knows the summary is incomplete.
func clipPreview(s string) string {
	if utf8.RuneCountInString(s) <= MaxPreview {
		return s
	}
	return string([]rune(s)[:MaxPreview]) + "\n… (preview truncated)"
}

// PreviewRequired reports whether an approval of this effect and audit class
// must carry a preview before the action runs (SPEC §18.2). The dangerous set
// comes from the classifier's closed labels (destructive and the P26
// classes); workspace writes need one too. Reads, and classes below
// destructive, are not dangerous.
func PreviewRequired(effect Effect, class string) bool {
	if effect == EffectRead {
		return false
	}
	if strings.HasPrefix(class, "shell:") {
		for _, label := range []string{"destructive", "irreversible", "monetary", "delegated_agent_bypass"} {
			if strings.Contains(class, label) {
				return true
			}
		}
		return false
	}
	return class == "write"
}

// Validate checks the request contract before an approver sees it: the origin
// is always one of the reviewed surfaces and a dangerous action always has a
// preview (SPEC §18.2, ADR-202).
func (r *ApprovalRequest) Validate() error {
	if !ValidOrigin(r.Origin) {
		return fmt.Errorf("tools: approval origin %q is not one of run|tui|acp|plan", r.Origin)
	}
	if PreviewRequired(r.Effect, r.Class) && strings.TrimSpace(r.Preview) == "" {
		return fmt.Errorf("tools: %s needs a preview before approval", r.Tool)
	}
	return nil
}

// Approver is the interactive approval channel (TUI, ACP, web UI, gateway).
type Approver interface {
	Approve(ctx context.Context, req ApprovalRequest) (Approval, error)
}

func toolSubject(tool, subject string) string { return tool + "\x00" + subject }

// previewArgs renders a bounded, deterministic preview of code-tool
// arguments for SPEC §18.2 (no file reads: the pipeline never touches the
// workspace before approval).
func previewArgs(name string, args json.RawMessage) string {
	switch name {
	case "write_file":
		var a struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if json.Unmarshal(args, &a) != nil {
			return ""
		}
		return previewWrite(a.Path, a.Content)
	case "edit_file":
		var a struct {
			Path  string `json:"path"`
			Edits []struct {
				OldString string `json:"old_string"`
				NewString string `json:"new_string"`
				NewText   string `json:"new_text"`
			} `json:"edits"`
		}
		if json.Unmarshal(args, &a) != nil {
			return ""
		}
		var b strings.Builder
		fmt.Fprintf(&b, "edit %s (%d edits)\n", a.Path, len(a.Edits))
		for i, e := range a.Edits {
			if i == 10 {
				fmt.Fprintf(&b, "+%d more edits\n", len(a.Edits)-i)
				break
			}
			old, new := e.OldString, e.NewString
			if old == "" {
				old, new = "(line range)", e.NewText
			}
			fmt.Fprintf(&b, "- %s\n+ %s\n", clipLine(old, 120), clipLine(new, 120))
		}
		return b.String()
	}
	return ""
}

// previewWrite shows the first lines of a full rewrite, bounded to 20 lines
// and 120 characters each.
func previewWrite(path, content string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "write %s (%d bytes)\n", path, len(content))
	for i, line := range strings.Split(content, "\n") {
		if i == 20 {
			fmt.Fprintf(&b, "+%d more lines\n", strings.Count(content, "\n")-i+1)
			break
		}
		fmt.Fprintf(&b, "+ %s\n", clipLine(line, 120))
	}
	return b.String()
}

func clipLine(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	// Cut at a rune boundary so the preview is always valid UTF-8.
	cut := n
	for cut > 0 && s[cut]&0xC0 == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

// authorize applies one prepared call's approval requirement. It is the only
// place that consults the approver or session approvals.
func (d *Dispatcher) authorize(ctx context.Context, p *Prepared) *Outcome {
	p.Env.Approved = false
	switch p.Decision.Requirement {
	case RequireAuto:
		p.authorized = true
		return nil
	case RequireDeniedPath:
		out := failure(CodeDeniedPath, "path matches deny_paths: "+strings.Join(p.Decision.Paths, ", "), "choose another path")
		return &out
	case RequireDeny:
		out := failure(CodeDenied, p.Call.Name+" is not allowed in this mode", "")
		out.Denied = true
		return &out
	case RequireOutside:
		if d.mode.Headless() {
			out := failure(CodeOutsideWorkspace, "path is outside the workspace: "+strings.Join(p.Decision.Paths, ", "), "headless runs never approve outside paths")
			return &out
		}
		return d.ask(ctx, p, true)
	case RequireAsk:
		return d.ask(ctx, p, false)
	}
	out := failure(CodeInternal, "unknown policy requirement", "")
	return &out
}

// ask consults a session approval or the approver for one prepared call.
func (d *Dispatcher) ask(ctx context.Context, p *Prepared, outside bool) *Outcome {
	dec := p.Decision
	if dec.AllowSession && d.sessionAllowed(p.Call.Name, dec.Paths) {
		p.Approved = outside
		p.authorized = true
		return nil
	}
	req := ApprovalRequest{
		Tool: p.Call.Name, CallID: p.Call.CallID, Effect: p.Effect, Class: dec.Class,
		Paths: dec.Paths, Preview: dec.Preview, Origin: d.origin, Tainted: d.tainted.Load(),
		AllowSession: dec.AllowSession,
	}
	// Fail closed: a dangerous action without a preview is never shown as
	// approvable (SPEC §18.2, ADR-202). ensurePreview normally fills it, so
	// this only fires when an Inspector returned an unusable decision.
	if err := req.Validate(); err != nil {
		out := failure(CodeDenied, err.Error(), "the call was not shown to the approver")
		out.Denied = true
		return &out
	}
	if d.approver == nil {
		out := failure(CodeDenied, "no approver is available", "interactive approval is required")
		out.Denied = true
		return &out
	}
	answer, err := d.approver.Approve(ctx, req)
	if err != nil {
		out := fromError(err)
		return &out
	}
	switch answer {
	case ApprovalAllowOnce:
		p.Approved = outside
		p.authorized = true
		return nil
	case ApprovalAllowSession:
		if req.AllowSession && len(req.Paths) > 0 {
			d.mu.Lock()
			for _, path := range req.Paths {
				d.sessions[toolSubject(req.Tool, path)] = true
			}
			d.mu.Unlock()
		}
		p.Approved = outside
		p.authorized = true
		return nil
	case ApprovalDeny:
	}
	code, msg := CodeDenied, "approval denied for "+p.Call.Name
	if outside {
		code, msg = CodeOutsideWorkspace, "outside-workspace access was not approved"
	}
	out := failure(code, msg, "")
	out.Denied = true
	return &out
}

// sessionAllowed reports an earlier allow_session for every exact subject.
func (d *Dispatcher) sessionAllowed(tool string, paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, p := range paths {
		if !d.sessions[toolSubject(tool, p)] {
			return false
		}
	}
	return true
}
