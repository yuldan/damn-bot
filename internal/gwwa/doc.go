// Package gwwa is the `damnbot.bridge/v1` client of the external WhatsApp Web
// bridge (ADR-034, SPEC §9.7.1). It carries the versioned JSON-RPC protocol,
// the inbound-only outbound guard and the GAP-GW-11 breaker verdict; it never
// links `whatsmeow`/`libsignal` and never fetches the WhatsApp Web protocol
// version at runtime (the bridge pins it and reports it in the handshake).
package gwwa
