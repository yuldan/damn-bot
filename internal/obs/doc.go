// Package obs serves the loopback observability endpoints of `damnbot serve`
// (SPEC §15 GAP-REL-17): `/healthz` liveness, `/readyz` readiness with named
// checks, and `/metrics` in Prometheus text 0.0.4 written without a client
// library (P21). It opens no connection: this is pull diagnostics, not
// telemetry.
package obs
