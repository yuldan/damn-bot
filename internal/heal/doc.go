// Package heal is the self-healing supervisor of Damn-Bot (SELF-HEALING.md):
// supervisor state machine (§1.4), watchdog (§2, `/healthz` ≤ 3 s), turn-journal
// recovery (§3.4/SPEC §11.11.8) via internal/ctxdb, and repairs (§4). Service
// installation (§1.5–§1.7) and the exit-70 path stay with `serve`.
package heal
