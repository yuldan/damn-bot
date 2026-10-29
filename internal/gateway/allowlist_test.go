package gateway_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/gateway"
)

// TestParseAllowlistJSONAndCSV is EC-PLAT-021: the JSON form is decoded, never
// split on its commas, and both forms normalise to the same sorted set.
func TestParseAllowlistJSONAndCSV(t *testing.T) {
	csv, err := gateway.ParseAllowlist("telegram:42, irc:alice  telegram:7")
	if err != nil {
		t.Fatalf("CSV: %v", err)
	}
	jsonList, err := gateway.ParseAllowlist(`["telegram:42", "irc:alice", "telegram:7", "telegram:42"]`)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	want := []gateway.AllowEntry{
		{Platform: "irc", ExternalID: "alice"},
		{Platform: "telegram", ExternalID: "42"},
		{Platform: "telegram", ExternalID: "7"},
	}
	if !slices.Equal(csv, want) || !slices.Equal(jsonList, want) {
		t.Fatalf("csv = %v, json = %v, want %v", csv, jsonList, want)
	}
	for _, empty := range []string{"", "   ", ","} {
		if got, err := gateway.ParseAllowlist(empty); err != nil || len(got) != 0 {
			t.Errorf("ParseAllowlist(%q) = %v, %v; want empty", empty, got, err)
		}
	}
}

// TestParseAllowlistRejectsInvalid: a malformed entry fails the whole value
// with a message naming the entry, so bootstrap writes no partial row set.
func TestParseAllowlistRejectsInvalid(t *testing.T) {
	for _, raw := range []string{
		`["telegram:42",]`,
		`telegram`,
		`telegram:`,
		`:42`,
		`telegram:4 2`,
		`telegram:42;irc:alice`,
		`["telegram:42"] trailing`,
		`["telegram:a b"]`,
		`[42]`,
		`[1.5]`,
		`[true]`,
		`telegram:_bad`,
		strings.Repeat("a", 129) + ":1",
	} {
		entries, err := gateway.ParseAllowlist(raw)
		if !errors.Is(err, gateway.ErrAllowlist) {
			t.Errorf("ParseAllowlist(%q) = %v, %v; want ErrAllowlist", raw, entries, err)
			continue
		}
		if !strings.Contains(err.Error(), "entry") && !strings.Contains(err.Error(), "JSON") {
			t.Errorf("ParseAllowlist(%q) error %q does not name the entry", raw, err)
		}
	}
}
