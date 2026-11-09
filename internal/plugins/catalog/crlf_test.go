package catalog

import (
	"errors"
	"strings"
	"testing"
)

// TestF8PluginCatalogCRLF proves the embedded corpus parses identically when a
// checkout rewrites it to CRLF (Windows): the carriage return must not become
// part of the description or the digest.
func TestF8PluginCatalogCRLF(t *testing.T) {
	t.Parallel()
	lf := strings.ReplaceAll(string(mustReadTSV(t)), "\r", "")
	crlf := strings.ReplaceAll(lf, "\n", "\r\n")
	c, err := Parse([]byte(crlf))
	if err != nil {
		t.Fatalf("Parse(CRLF corpus): %v", err)
	}
	if c.Count() != TotalCount {
		t.Fatalf("Count = %d, want %d", c.Count(), TotalCount)
	}
	lfCat, err := Parse([]byte(lf))
	if err != nil {
		t.Fatalf("Parse(LF corpus): %v", err)
	}
	if c.Entries()[0].Description != lfCat.Entries()[0].Description {
		t.Fatal("CRLF parse changed the first description")
	}
	// A checkout that rewrote an already-CRLF blob produces `\r\r\n`; it must
	// parse the same canonical LF corpus.
	double := strings.ReplaceAll(lf, "\n", "\r\r\n")
	if again, derr := Parse([]byte(double)); derr != nil || again.Count() != TotalCount {
		t.Fatalf("Parse(double CRLF) = (%v, %v)", again, derr)
	}
	// A broken row still fails, so normalisation cannot hide tampering.
	bad := strings.Replace(crlf, "Control real iPhones", "Control fake iPhones", 1)
	if _, err := Parse([]byte(bad)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Parse(tampered CRLF) = %v, want ErrInvalid", err)
	}
}
