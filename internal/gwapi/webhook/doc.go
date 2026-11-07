// Package webhook implements the generic webhook receiver (PLAT-029) and the
// persistent subscription table shared with the Microsoft Graph surface
// (GW-011/GW-012). The receiver verifies its HMAC, dedupes deliveries and
// dispatches through the gateway contract; the store owns no secret value.
package webhook
