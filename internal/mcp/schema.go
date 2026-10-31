package mcp

import (
	"encoding/json"
	"fmt"
)

// NormalizeSchema turns one MCP inputSchema into the object schema the tool
// pipeline accepts: root type object, properties always present, and
// additionalProperties=false when the server omitted it (SPEC §9.6). A schema
// that cannot satisfy the contract is refused, never silently loosened.
func NormalizeSchema(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: missing schema", ErrInvalidSchema)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSchema, err)
	}
	if root == nil {
		return nil, fmt.Errorf("%w: schema is not an object", ErrInvalidSchema)
	}
	if typ, ok := root["type"]; ok {
		var name string
		if err := json.Unmarshal(typ, &name); err != nil || name != "object" {
			return nil, fmt.Errorf("%w: root type must be object", ErrInvalidSchema)
		}
	} else {
		root["type"] = json.RawMessage(`"object"`)
	}
	if props, ok := root["properties"]; ok {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(props, &object); err != nil || object == nil {
			return nil, fmt.Errorf("%w: properties must be an object", ErrInvalidSchema)
		}
	} else {
		root["properties"] = json.RawMessage(`{}`)
	}
	if _, ok := root["additionalProperties"]; !ok {
		root["additionalProperties"] = json.RawMessage(`false`)
	}
	out, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSchema, err)
	}
	return out, nil
}
