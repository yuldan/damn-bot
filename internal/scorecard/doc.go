// Package scorecard is the F4 weekly scorecard job (SPEC §18.5, SPEC
// §3.2.4:307: 500 of the `batch/evals/trajectory + scorecard job` line; the
// remaining 2.500 stay with F8).
//
// The job runs once per ISO week (Monday 00:00 UTC), claims the week through a
// Store seam so a restart or a second scheduler cannot run it twice, and asks a
// Runner for the deterministic eval harness result. A missing Runner is
// reported as `skipped` with "runner tidak tersedia"; this package never
// fabricates a score. A failed run or a drop above DefaultDropThreshold over
// the previous successful run enqueues one alarm into the gateway outbox seam
// (owner chat from DAMNBOT_GATEWAY_OWNER, parsed by OwnerFromEnv); without an
// owner chat the alarm is `skipped` and no message is written.
//
// SPEC §18.5: "Job cron bawaan `scorecard-weekly` … Alarm Telegram ke owner
// bila ada dimensi kalah atau regresi > 5% vs run sebelumnya". The weekly slot
// itself is left open by the SPEC (it only says "mingguan"); this package
// fixes it at Monday 00:00 UTC so the run key and the SCORECARD artifact share
// one boundary.
package scorecard
