package assemble

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// marshalNoEscape is json.Marshal without HTML escaping, matching
// internal/provider (content bytes verbatim; no \u003c inflation).
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, fmt.Errorf("assemble: encode: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
