//go:build acpe2e

package main

import "os"

// e2eProviderBase lets the binary used by internal/e2e's ACP client-fs
// scenarios reach the fake provider over plain HTTP on every OS: Go trusts
// SSL_CERT_FILE only on Unix other than macOS, so a TLS MITM provider is not
// portable. The acpe2e tag is never part of a release build.
func e2eProviderBase() string { return os.Getenv("DAMNBOT_E2E_BASE_URL") }
