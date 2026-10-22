// Package assemble builds the prompt tiers of one turn (SPEC §4.5 and §6;
// ADR-066, ADR-067, ADR-082, ADR-114, ADR-121).
//
// The request is assembled in SPEC §4.5 tier order:
//
//	stable tier    identity + rules; rule 3 (deferred catalog) is rendered
//	               exactly when the catalog is, repo_map with [StableOptions]
//	catalog        deferred module groups from the module registry, sorted,
//	               ≤ 24 lines + overflow, ≤ 600 tokens
//	context        facts, workspace snapshot, sandboxed context files,
//	               retrieved memory (pinned first) and skills, each with its
//	               SPEC §6 sub-cap; rendered once per session and frozen
//	tools          eager schemas, sorted, phase variant
//	summaries      newest active compaction summaries ≤ 3.000 tokens
//	retrieval      per-turn hits, each wrapped <untrusted>, ≤ 800 tokens
//	history        append-only; the frozen volatile tier (time, budget,
//	               todos, new names, notices) rides on the last user message
//	runtime        <runtime> suffix for the latest tool_result (ADR-082)
//
// Token accounting is a single formula owned by internal/budget (COST-09).
// Because the import graph keeps assemble independent of budget, the formula
// is injected as [Deps.Tokens]; the composition root passes
// budget.Count(...).Raw and tests pass an ASCII-equivalent counter.
//
// Nothing outside internal/loop/event is imported (E-01). Context files are
// opened through a [FileReader] the composition root implements over
// internal/sandbox (SPEC §14.2, ADR-097); memories and skills come from a
// [Retriever] over internal/memory and internal/skills (ADR-120); catalog
// groups and summaries are caller data mapped from internal/modules and
// internal/compaction. Every piece of caller-supplied tier text is passed
// through [Deps.Scrub] before it can enter the prompt, so context files and
// their paths, facts, workspace text, memory, skills, catalog lines,
// summaries, retrieval hits and volatile notices are redacted; one-line fields
// (catalog lines, file paths, memory and skill lines) also lose line breaks
// and control/format runes such as ESC and U+202E. Untrusted text is wrapped
// so that cutting it never leaves a wrapper open.
//
// The eager tool schemas are input, not data owned here: assemble filters the
// caller's list to the phase's schema variant (the F1 shell schema has no
// background parameter) and serializes it in the openai-chat wire shape.
package assemble
