// Package acp is the transport core of the Agent Client Protocol server
// (SPEC §13.3): LF-delimited JSON-RPC 2.0 framing with a hard cap, a write
// deadline, sanitised error mapping, and per-session state. It performs no
// I/O beyond the injected reader/writer and imports only the standard
// library; the composition root owns stdio, config, and the loop (E-03).
package acp
