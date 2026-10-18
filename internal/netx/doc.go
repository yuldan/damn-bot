// Package netx is the only package that creates HTTP clients, transports,
// and dialers (ENGINEERING E-05); it also owns the SSE framing parser
// (E-35). Retry, backoff, and the circuit breaker live in netx/retry (P6,
// E-32).
package netx
