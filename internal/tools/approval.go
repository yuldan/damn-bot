package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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
	Tainted bool
	// AllowSession is false for shell (ADR-088 butir 1), execute_code and
	// P26 classes (irreversible/monetary/delegated_agent_bypass).
	AllowSession bool
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
