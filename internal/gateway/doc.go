// Package gateway is the core of the F4 gateway (SPEC §3.2.4 `gateway core`): platform
// adapters live in internal/gateway/<platform>, with Telegram in internal/gateway/telegram.
// This package is the F4 scaffold only; no API is implemented yet, so the architecture matrix
// (internal/archtest/allowed.go) leaves its edges empty until the first real implementation.
package gateway
