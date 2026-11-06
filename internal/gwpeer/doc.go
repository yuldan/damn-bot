// Package gwpeer implements the F7 peer gateway surfaces: bot-to-bot direct
// messages across machines (GW-013), hosted rooms (GW-014), profile relay
// (GW-015), profile multiplexing and its connection budget (GW-016,
// EC-STATE-004), scale-to-zero (GW-022), the channel directory (GW-023),
// managed-bot provisioning (GW-025), CLI-session handoff (GW-026/SLASH-012),
// and the external approval transport (EXEC-016).
//
// The package is deliberately independent of the Telegram adapter: a peer
// message enters through an authenticated HTTP surface and leaves through a
// dispatcher the composition root supplies. Identity is an Ed25519 key pair,
// the transport token comes from a secret source (never from the config file),
// and every message carries a nonce plus a timestamp so a replay is rejected
// before any dispatch.
package gwpeer
