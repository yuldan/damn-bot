// Package webhook implements the generic webhook receiver (PLAT-029, GW-011)
// and the Microsoft Graph notification surface (PLAT-030, GW-012).
//
// A webhook is an untrusted third-party callback: every request is HMAC
// verified in constant time, bounded in size, deduplicated by delivery id and
// never logged with its payload. The receiver only admits a message; the core
// gateway performs admission and the webhook toolset (TSET-061) restricts the
// resulting turn to web/vision/clarify (EC-SEC-006).
package webhook
