// Package gateway is the core of the F4 gateway (SPEC §3.2.4 `gateway core`,
// §9.7 `Akses & multi-user`): deny-by-default admission, identity and role,
// per-(user, chat) scope keys, and inbound dedupe. Platform adapters live in
// internal/gateway/<platform>, with Telegram in internal/gateway/telegram; the
// poller, worker and outbox follow in later F4 slices.
//
// The package never imports ctxdb, provider, exec or codetools (E-03). Storage
// arrives through the small consumer-side interfaces IdentityStore and
// InboxStore (E-09); internal/ctxdb/gwstore implements them and cmd/damnbot
// wires them (ADR-074).
package gateway
