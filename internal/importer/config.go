package importer

import (
	"fmt"
	"strings"
	"unicode"
)

type mapKind int

const (
	kindString mapKind = iota
	kindInt
	kindStrings
)

type mapping struct {
	section string
	key     string
	kind    mapKind
	min     int
	max     int
}

// configMappings is the explicit Hermes → Damn-Bot table. Every other leaf is
// reported as skipped with a reason; a missing entry here is never a silent
// drop, it is a reported one.
func configMappings() map[string]mapping {
	return map[string]mapping{
		"model.default":            {section: "provider", key: "model", kind: kindString},
		"terminal.timeout":         {section: "exec", key: "default_timeout_s", kind: kindInt, min: 1, max: 600},
		"agent.max_turns":          {section: "loop", key: "max_iterations", kind: kindInt, min: 1, max: 40},
		"memory.memory_char_limit": {section: "memory", key: "total_max_chars", kind: kindInt, min: 1},
		"skills.external_dirs":     {section: "skills", key: "external_dirs", kind: kindStrings},
	}
}

func decodeMapping(raw []byte, path string) (map[string]any, map[string]string, error) {
	return decodeYAML11(raw, path)
}

func (i *Importer) collectConfig(c *collected) error {
	if c.configDoc == nil {
		return nil
	}
	mappings := configMappings()
	var walk func(prefix string, m map[string]any) error
	walk = func(prefix string, m map[string]any) error {
		for key, value := range m {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			if path == "mcp_servers" {
				continue // owned by the MCP source
			}
			if child, ok := value.(map[string]any); ok {
				if err := walk(path, child); err != nil {
					return err
				}
				continue
			}
			i.mapConfigItem(path, value, mappings, c)
		}
		return nil
	}
	if err := walk("", c.configDoc); err != nil {
		return err
	}
	return nil
}

func (i *Importer) mapConfigItem(path string, value any, mappings map[string]mapping, c *collected) {
	if path == "memory.provider" {
		if text, ok := value.(string); ok {
			switch strings.ToLower(text) {
			case "byterover", "retaindb":
				c.report.Warnings = append(c.report.Warnings,
					fmt.Sprintf("memory provider %q is not ported; memory continues natively", text))
			}
		}
	}
	if c.reject(SourceConfig, path) {
		return
	}
	if isSecretPath(path) {
		text, ok := value.(string)
		if !ok || text == "" {
			c.report.skip(SourceConfig, path, "secret value is not a non-empty string")
			return
		}
		c.queueSecret(secretName(path), text)
		c.report.skip(SourceConfig, path, "secret kept in the keychain by reference only")
		return
	}
	m, ok := mappings[path]
	if !ok {
		c.report.skip(SourceConfig, path, "no Damn-Bot equivalent")
		return
	}
	converted, clamped, reason := convertValue(m, value, c.configAmbiguous[path])
	if reason != "" {
		c.report.skip(SourceConfig, path, reason)
		return
	}
	if clamped != nil {
		c.report.Clamps = append(c.report.Clamps, *clamped)
	}
	c.entries = append(c.entries, ConfigEntry{Section: m.section, Key: m.key, Value: converted})
	c.report.item(SourceConfig)
}

func convertValue(m mapping, value any, raw string) (any, *Clamp, string) {
	switch m.kind {
	case kindString:
		text, ok := value.(string)
		if ok {
			return text, nil, ""
		}
		if raw != "" {
			return nil, nil, fmt.Sprintf("ambiguous YAML 1.1 scalar on a string key; original text %q", raw)
		}
		return nil, nil, "expected a string"
	case kindStrings:
		items, ok := value.([]any)
		if !ok {
			return nil, nil, "expected a list of strings"
		}
		out := make([]string, 0, len(items))
		for _, item := range items {
			text, ok := item.(string)
			if !ok {
				return nil, nil, "expected a list of strings"
			}
			out = append(out, text)
		}
		return out, nil, ""
	case kindInt:
		return convertInt(m, value)
	}
	return nil, nil, "unknown mapping kind"
}

func convertInt(m mapping, value any) (any, *Clamp, string) {
	n, ok := value.(int)
	if !ok {
		return nil, nil, "expected an integer"
	}
	clamped := n
	if m.min != 0 && n < m.min {
		clamped = m.min
	}
	if m.max != 0 && n > m.max {
		clamped = m.max
	}
	entry := ConfigEntry{Section: m.section, Key: m.key, Value: clamped}
	if clamped != n {
		key := entry.Section + "." + entry.Key
		return clamped, &Clamp{Key: key, From: n, To: clamped}, ""
	}
	return clamped, nil, ""
}

func isSecretPath(path string) bool {
	last := path
	if idx := strings.LastIndexByte(path, '.'); idx >= 0 {
		last = path[idx+1:]
	}
	switch strings.ToLower(last) {
	case "api_key", "token", "password", "secret", "client_secret", "access_token":
		return true
	default:
		return false
	}
}

func secretName(path string) string {
	var b strings.Builder
	b.WriteString("hermes.")
	for _, r := range path {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}
