package gateway

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"
)

// ErrAllowlist rejects a DAMNBOT_GATEWAY_ALLOW/OWNER value that is not a JSON
// list or a whitespace/comma separated list of platform:id entries.
var ErrAllowlist = errors.New("gateway: invalid allowlist")

// AllowEntry is one parsed allowlist identity (a gateway_identities key).
type AllowEntry struct {
	Platform   string
	ExternalID string
}

// ParseAllowlist parses an operator identity list (EC-PLAT-021): a JSON array
// of strings or a comma/whitespace separated list of `platform:id` entries.
// The result is sorted and deduplicated for deterministic bootstrap writes.
func ParseAllowlist(raw string) ([]AllowEntry, error) {
	items, err := allowItems(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	entries := make([]AllowEntry, 0, len(items))
	for i, item := range items {
		platform, id, ok := strings.Cut(strings.TrimSpace(item), ":")
		if !ok || !validIDToken(platform) || !validIDToken(id) {
			return nil, fmt.Errorf("%w: entry %d %q is not platform:id", ErrAllowlist, i+1, item)
		}
		entries = append(entries, AllowEntry{Platform: platform, ExternalID: id})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Platform != entries[j].Platform {
			return entries[i].Platform < entries[j].Platform
		}
		return entries[i].ExternalID < entries[j].ExternalID
	})
	return slices.Compact(entries), nil
}

// allowItems splits raw into entries; a leading '[' selects the JSON form.
func allowItems(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	if !strings.HasPrefix(raw, "[") {
		return strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || unicode.IsSpace(r) }), nil
	}
	var elems []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &elems); err != nil {
		return nil, fmt.Errorf("%w: JSON list: %w", ErrAllowlist, err)
	}
	items := make([]string, 0, len(elems))
	for i, elem := range elems {
		var s string
		if err := json.Unmarshal(elem, &s); err != nil {
			return nil, fmt.Errorf("%w: JSON entry %d must be a string id", ErrAllowlist, i+1)
		}
		items = append(items, s)
	}
	return items, nil
}

// validIDToken is the identity grammar of SPEC §12 (mirrors
// gwstore.ValidIdentity, which cannot be imported here, E-03).
func validIDToken(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := range len(id) {
		c := id[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && (c == '_' || c == '.' || c == ':' || c == '@' || c == '-'):
		default:
			return false
		}
	}
	return true
}
