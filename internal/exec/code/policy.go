package code

import (
	"fmt"
	"slices"
)

// Mode is the approval mode (SPEC §12 approvals.mode).
type Mode string

// Approval modes.
const (
	ModeAsk      Mode = "ask"
	ModeAutoEdit Mode = "auto-edit"
	ModeAuto     Mode = "auto"
)

// Requirement is the policy answer for one execute_code call.
type Requirement string

// Requirements.
const (
	RequireAsk  Requirement = "ask"
	RequireAuto Requirement = "auto"
	RequireDeny Requirement = "deny"
)

// PolicyInput is everything ADR-089 lets decide one call; no LLM involved.
type PolicyInput struct {
	Mode Mode
	// Headless: a non-interactive run; only --approval auto may auto-approve.
	Headless bool
	// AutoLanguages is exec.code_auto_languages (never shell languages).
	AutoLanguages []string
	// WorkspaceTrusted and Tainted are the ADR-079 conditions for auto.
	WorkspaceTrusted bool
	Tainted          bool
	// WroteWithoutApproval: an unapproved write happened in this session.
	WroteWithoutApproval bool
}

// Decision is the policy outcome with its reason.
type Decision struct {
	Requirement Requirement
	Reason      string
}

// ShellLanguage reports the languages that follow the shell model instead of
// code_auto_languages (ADR-089 butir 1).
func ShellLanguage(lang Language) bool {
	switch lang {
	case Python, JavaScript, Go:
		return false
	case "bash", "sh", "zsh", "pwsh", "powershell", "cmd":
		return true
	}
	return false
}

// Decide applies ADR-089 / SPEC §14.1. An unsupported language is refused
// before approval; shell languages never auto-approve through the code
// allowlist.
func Decide(lang Language, in PolicyInput) (Decision, error) {
	if _, _, err := interpreter(lang); err != nil {
		return Decision{Requirement: RequireDeny, Reason: err.Error()}, err
	}
	if ShellLanguage(lang) {
		return Decision{Requirement: RequireDeny, Reason: "shell language uses the shell approval path"}, nil
	}
	allowed := slices.Contains(in.AutoLanguages, string(lang))
	autoOK := allowed && in.WorkspaceTrusted && !in.Tainted && !in.WroteWithoutApproval
	switch in.Mode {
	case ModeAsk, ModeAutoEdit:
		return Decision{Requirement: RequireAsk, Reason: "mode " + string(in.Mode)}, nil
	case ModeAuto:
		if autoOK {
			return Decision{Requirement: RequireAuto, Reason: "language in exec.code_auto_languages and ADR-079 conditions hold"}, nil
		}
		return Decision{Requirement: RequireAsk, Reason: autoReason(allowed, in)}, nil
	default:
		return Decision{Requirement: RequireDeny, Reason: "unknown approval mode " + string(in.Mode)}, nil
	}
}

// autoReason explains why the auto path did not apply.
func autoReason(allowed bool, in PolicyInput) string {
	switch {
	case !allowed:
		return "language not in exec.code_auto_languages"
	case !in.WorkspaceTrusted:
		return "workspace is untrusted"
	case in.Tainted:
		return "turn is tainted"
	case in.WroteWithoutApproval:
		return "session wrote without approval"
	default:
		return "auto conditions not met"
	}
}

// DecideHeadless is the headless variant: denied unless mode auto, the
// language is in the allowlist, and every auto condition holds. A caller
// that has no approver MUST use this function.
func DecideHeadless(lang Language, in PolicyInput) (Decision, error) {
	in.Headless = true
	d, err := Decide(lang, in)
	if err != nil || d.Requirement != RequireAuto {
		if d.Requirement != RequireDeny {
			d = Decision{Requirement: RequireDeny, Reason: fmt.Sprintf("headless: %s", d.Reason)}
		}
	}
	return d, err
}
