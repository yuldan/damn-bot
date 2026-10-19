package assemble

import "strings"

// Runtime suffix caps from ADR-082: n/40 6, no_progress 14, process notice
// 20, whole suffix 40 tokens.
const (
	CapRuntime           = 40
	CapRuntimeProgress   = 6
	CapRuntimeNoProgress = 14
	CapRuntimeProcess    = 20
)

// RuntimeInput is the per-iteration state of the <runtime> suffix. It is not
// part of the volatile tier: the loop stores the rendered suffix in the latest
// tool_result payload so re-rendering is byte-identical (ADR-067 butir 2).
type RuntimeInput struct {
	Iteration     int
	MaxIterations int
	NoProgress    bool
	ProcessNotice string
}

// RuntimeParts is the rendered suffix per component.
type RuntimeParts struct {
	Progress   string
	NoProgress string
	Process    string
}

// Runtime renders the suffix for one tool_result. Components are trimmed in
// reverse priority until the whole <runtime> element fits [CapRuntime].
func (a *Assembler) Runtime(in RuntimeInput) RuntimeParts {
	parts := RuntimeParts{}
	if in.MaxIterations > 0 {
		parts.Progress = clip(itoa(in.Iteration)+"/"+itoa(in.MaxIterations), CapRuntimeProgress, "", a.tokens)
	}
	if in.NoProgress {
		parts.NoProgress = clip("no_progress: identical call repeated", CapRuntimeNoProgress, "", a.tokens)
	}
	if in.ProcessNotice != "" {
		parts.Process = clip("proc: "+a.scrub(in.ProcessNotice), CapRuntimeProcess, "", a.tokens)
	}
	return parts
}

// RuntimeString renders "<runtime>…</runtime>", or "" when every component is
// empty.
func (a *Assembler) RuntimeString(parts RuntimeParts) string {
	text := renderRuntime(parts)
	if a.tokens.Count(text) <= CapRuntime {
		return text
	}
	parts.Process = ""
	if text = renderRuntime(parts); a.tokens.Count(text) <= CapRuntime {
		return text
	}
	parts.NoProgress = ""
	if text = renderRuntime(parts); a.tokens.Count(text) <= CapRuntime {
		return text
	}
	return "<runtime>" + clip(parts.Progress, CapRuntime-4, "", a.tokens) + "</runtime>"
}

func renderRuntime(parts RuntimeParts) string {
	list := []string{parts.Progress, parts.NoProgress, parts.Process}
	out := list[:0]
	for _, p := range list {
		if p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return ""
	}
	return "<runtime>" + strings.Join(out, "\n") + "</runtime>"
}
