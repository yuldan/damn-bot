// Package obs serves the F4 observability endpoints `/healthz`, `/readyz`, and `/metrics` (SPEC
// §3.2.4, `heal` + `obs`). It is split from internal/heal so the supervisor keeps its core budget.
// This package is the F4 scaffold only; no API is implemented yet, so the architecture matrix
// (internal/archtest/allowed.go) leaves its edges empty until the first real implementation.
package obs
