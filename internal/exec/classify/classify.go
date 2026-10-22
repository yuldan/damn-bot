// Package classify labels shell commands with a risk class and flags for
// the approval prompt (EXEC-AND-CODING §A.8, ADR-069, ADR-079, ADR-088,
// ADR-106). Labels never grant auto-approval: the only auto path for shell
// is an exact shell_allow match (MatchAllow) gated by Decide. The package is
// pure: the file system is reached only through an injected PathResolver.
package classify

// Class is the risk label; a command takes the highest class of all its
// simple commands. Classes from Irreversible up are P26.
type Class uint8

// Risk classes in approval order (ADR-069; ADR-106 butir 2 orders unknown
// below destructive, both prompting identically).
const (
	Mutating Class = iota
	Unknown
	Destructive
	Irreversible
	Monetary
	DelegatedAgentBypass
)

// String returns the audit name of c.
func (c Class) String() string {
	return [...]string{"mutating", "unknown", "destructive", "irreversible", "monetary", "delegated_agent_bypass"}[c]
}

// P26 reports whether c is never auto-approved by any mechanism (SPEC P26).
func (c Class) P26() bool { return c >= Irreversible }

// Flag is a bitset of labels that always apply regardless of class.
type Flag uint8

// Flags (ADR-069, ADR-088 butir 2, ADR-081).
const (
	DeniedPath Flag = 1 << iota
	OutsideRead
	Privileged
	Network
	ReferencesSecret
)

// Names returns the audit names of the set flags in bit order.
func (f Flag) Names() []string {
	var out []string
	for i, n := range [...]string{"denied_path", "outside_read", "privileged", "network", "references_secret"} {
		if f&(1<<i) != 0 {
			out = append(out, n)
		}
	}
	return out
}

// Dialect selects the parser: mvdan bash/POSIX, or the token lexer for
// PowerShell and cmd.exe.
type Dialect uint8

// Dialects.
const (
	Bash Dialect = iota
	POSIX
	Pwsh
	Cmd
)

// PathResolver reaches the file system for operand resolution; nil keeps
// resolution lexical (globs then check only their static prefix).
type PathResolver interface {
	// EvalSymlinks resolves symlinks in every existing component of an
	// absolute path, keeping a missing tail lexical.
	EvalSymlinks(p string) (string, error)
	// Glob expands an absolute pattern against the file system.
	Glob(pattern string) ([]string, error)
}

// Env is the classification context. Paths are absolute.
type Env struct {
	Roots         []string // session root plus add_dirs
	Cwd           string   // call cwd; empty = Roots[0]
	Home          string
	BlobDir       string   // reads below it are not outside_read
	Venv          string   // active virtualenv, if any
	DenyPaths     []string // deny_paths patterns; "~" expands to Home
	SecretVars    []string // exec.env names bound to keychain refs
	MonetaryHosts []string // security.monetary_hosts
	// MonetaryPaths are directories of skills declaring side_effects:
	// [payment] (SEC-15): a command that runs or names a file below one is
	// Monetary.
	MonetaryPaths []string
	// MonetaryKey canonicalizes a path for the payment-skill comparison:
	// absolute, symlinks resolved, long form, and case-folded on a
	// case-insensitive filesystem. nil keeps the lexical comparison.
	MonetaryKey func(string) string
	Resolver    PathResolver
	Dialect     Dialect
}

// Verdict is the classification result (audit payload exec.class/flags/
// reasons, ADR-069).
type Verdict struct {
	Class       Class
	Flags       Flag
	Reasons     []string   // at most maxReasons, first-seen order
	Argv        [][]string // every simple command; non-literal words as source
	NonMutating bool       // best-effort; only ever restricts (plan mode)
}

const (
	maxReasons   = 5
	maxReasonLen = 120
	maxDepth     = 8 // nested bash -c / eval / wrappers
)
