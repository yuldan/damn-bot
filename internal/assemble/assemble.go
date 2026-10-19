package assemble

import "fmt"

// Deps is the explicit dependency set of an [Assembler] (E-11).
type Deps struct {
	// Tokens counts prompt tokens. Required; the composition root passes the
	// COST-09 estimator (budget.Count(...).Raw).
	Tokens Counter
	// Scrub redacts secrets from every caller-supplied text before it can
	// enter the prompt. Required; the composition root passes
	// redact.Redactor.String. Context files are additionally read through the
	// sandbox [FileReader], never os.Open.
	Scrub func(string) string
}

// Assembler renders prompt tiers with injected token counting and redaction.
// It is immutable and safe for concurrent use.
type Assembler struct {
	tokens   Counter
	scrubber func(string) string
}

// New validates deps and returns an Assembler. A missing dependency is
// [ErrDeps]; there is no production fallback (E-11).
func New(deps Deps) (*Assembler, error) {
	if deps.Tokens == nil {
		return nil, fmt.Errorf("assemble: Tokens: %w", ErrDeps)
	}
	if deps.Scrub == nil {
		return nil, fmt.Errorf("assemble: Scrub: %w", ErrDeps)
	}
	return &Assembler{tokens: deps.Tokens, scrubber: deps.Scrub}, nil
}

func (a *Assembler) scrub(s string) string {
	if s == "" {
		return ""
	}
	return a.scrubber(s)
}
