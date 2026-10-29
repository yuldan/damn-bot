// Package cron is the F4 scheduled-job library started only by `serve`
// (SPEC §9.8): 5-field schedules with an injected clock, SELF-HEALING §6
// catch-up, one claim per slot, script jobs through internal/exec, and a
// Deliverer seam. `serve` supplies the store, outbox, timer and Executor.
package cron
