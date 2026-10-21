// Package assemble builds the prompt tiers of one turn (SPEC §4.5 and §6;
// ADR-066, ADR-067, ADR-082, ADR-106).
//
// F1 scope is the three stable parts of the system message and the frozen
// volatile tier:
//
//	stable tier    identity + rules; rule 3 (deferred catalog) is rendered
//	               exactly when the catalog is, repo_map with [StableOptions]
//	catalog        deferred module groups from the module registry, sorted,
//	               ≤ 24 lines + overflow, ≤ 600 tokens (ADR-121)
//	context        facts, workspace snapshot, sandboxed context files, memory,
//	               skills, each with its SPEC §6 sub-cap
//	volatile       time, remaining budget, todos, new names, notices; rendered
//	               once per turn and never re-rendered (ADR-067)
//	runtime        <runtime> suffix for the latest tool_result (ADR-082)
//
// Token accounting is a single formula owned by internal/budget (COST-09).
// Because the import graph keeps assemble independent of budget, the formula
// is injected as [Deps.Tokens]; the composition root passes
// budget.Count(...).Raw and tests pass an ASCII-equivalent counter.
//
// Context files are never opened here: [SessionView.Context] carries a
// [FileReader] that the composition root implements over internal/sandbox
// (SPEC §14.2, ADR-097). Every piece of caller-supplied tier text is passed
// through [Deps.Scrub] before it can enter the prompt, so context files and
// their paths, facts, workspace text, memory, skills, catalog lines and
// volatile notices are redacted; one-line fields (catalog lines, file paths)
// also lose line breaks and control/format runes such as ESC and U+202E.
//
// The F1 eager tool schemas are input, not data owned here: assemble filters the
// caller's list to the phase's schema variant (the F1 shell schema has no
// background parameter) and serializes it in the openai-chat wire shape.
package assemble
