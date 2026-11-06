// Package gwplat is the shared contract of the F7 platform adapters (ADR-326,
// SPEC §3.2.4): closed descriptor registry, explicit config+secret activation,
// the outq/render outbound seam, core mention policy, replay dedupe and
// reconnect backoff. Adapters live in subpackages and never read the
// environment, pace with time.Sleep, or print a secret.
package gwplat
