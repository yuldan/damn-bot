// Package auto is the F8 auxiliary-automation framework (AUTO-001..004,
// AUTO-012..016, AUTO-030..032). Every feature is opt-in: the zero
// configuration enables nothing and makes no provider call.
//
// The package owns four seams only: the closed feature registry, per-feature
// daily budgets (tokens and micro-USD), the non-critical scheduler, and the
// single call path that tags an auxiliary request with purpose=auto and
// feature=<id>. Feature policies (title, suggestions, cleanup, review,
// curator, goals, loops, heartbeat, webhooks) live in sibling files of this
// package and never touch the critical turn path.
package auto
