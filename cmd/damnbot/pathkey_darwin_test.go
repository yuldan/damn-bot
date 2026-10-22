//go:build darwin

package main

import "testing"

// TestMonetaryKeyNFC: NFD and NFC spellings of the same name share a key on
// APFS (SEC10-16).
func TestMonetaryKeyNFC(t *testing.T) {
	t.Parallel()
	nfc := "/tmp/caf\u00e9/skill"
	nfd := "/tmp/cafe\u0301/skill"
	if monetaryKey(nfc) != monetaryKey(nfd) {
		t.Fatalf("NFC %q != NFD %q", monetaryKey(nfc), monetaryKey(nfd))
	}
}
