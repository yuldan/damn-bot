package importer

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// decodeYAML11 decodes one Hermes configuration with the plain-scalar
// resolution PyYAML applies (YAML 1.1). It is deliberately scoped to the
// importer: go.yaml.in/yaml/v3 remains a YAML 1.2 decoder everywhere else.
func decodeYAML11(raw []byte, path string) (map[string]any, map[string]string, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return nil, nil, syntaxError(path, err)
	}
	ambiguous := map[string]string{}
	if len(root.Content) > 0 {
		resolveYAML11(root.Content[0], "", ambiguous)
	}
	var doc map[string]any
	if err := root.Decode(&doc); err != nil {
		return nil, nil, syntaxError(path, err)
	}
	return doc, ambiguous, nil
}

// resolveYAML11 rewrites only plain scalars. Quoted scalars keep their YAML
// string style and are never recorded as ambiguous.
func resolveYAML11(n *yaml.Node, path string, ambiguous map[string]string) {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, child := range n.Content {
			resolveYAML11(child, path, ambiguous)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key, value := n.Content[i], n.Content[i+1]
			childPath := key.Value
			if path != "" {
				childPath = path + "." + key.Value
			}
			resolveYAML11(value, childPath, ambiguous)
		}
	case yaml.SequenceNode:
		for i, child := range n.Content {
			resolveYAML11(child, fmt.Sprintf("%s[%d]", path, i), ambiguous)
		}
	case yaml.ScalarNode:
		resolveScalar11(n, path, ambiguous)
	case yaml.AliasNode:
		return
	}
}

func resolveScalar11(n *yaml.Node, path string, ambiguous map[string]string) {
	if n.Style != 0 {
		return
	}
	raw := n.Value
	if n.Tag == "!!timestamp" {
		recordAmbiguous(path, raw, ambiguous)
		return
	}
	switch n.Tag {
	case "!!float":
		if _, ok := yaml11Float(raw); !ok {
			n.Tag, n.Value = "!!str", raw
			return
		}
		recordAmbiguous(path, raw, ambiguous)
	case "!!int":
		if _, ok := yaml11Int(raw); !ok {
			n.Tag, n.Value = "!!str", raw
			return
		}
		recordAmbiguous(path, raw, ambiguous)
	case "!!bool":
		recordAmbiguous(path, raw, ambiguous)
	}
	if tag, value, ok := yaml11Scalar(raw); ok {
		n.Tag, n.Value = tag, value
		recordAmbiguous(path, raw, ambiguous)
	}
}

func recordAmbiguous(path, raw string, ambiguous map[string]string) {
	if path != "" {
		ambiguous[path] = raw
	}
}

// yaml11Scalar resolves plain scalars whose YAML 1.1 and YAML 1.2 readings
// differ; true/false stay YAML 1.2 bools and need no rewrite.
func yaml11Scalar(raw string) (tag, value string, ok bool) {
	switch strings.ToLower(raw) {
	case "y", "yes", "on":
		return "!!bool", "true", true
	case "n", "no", "off":
		return "!!bool", "false", true
	}
	if n, ok := yaml11Int(raw); ok {
		return "!!int", strconv.FormatInt(n, 10), true
	}
	if strings.Contains(raw, ":") {
		if f, ok := yaml11Float(raw); ok {
			return "!!float", strconv.FormatFloat(f, 'g', -1, 64), true
		}
	}
	if yaml11Timestamp(raw) {
		return "!!timestamp", raw, true
	}
	return "", "", false
}

func yaml11Sign(raw string) (int64, string) {
	if strings.HasPrefix(raw, "-") {
		return -1, raw[1:]
	}
	if strings.HasPrefix(raw, "+") {
		return 1, raw[1:]
	}
	return 1, raw
}

func yaml11Int(raw string) (int64, bool) {
	sign, s := yaml11Sign(raw)
	if s == "" {
		return 0, false
	}
	switch {
	case strings.HasPrefix(s, "0x"):
		return yaml11Base(s[2:], 16, sign)
	case strings.HasPrefix(s, "0b"):
		return yaml11Base(s[2:], 2, sign)
	case len(s) > 1 && s[0] == '0' && s[1] != 'o':
		return yaml11Base(s[1:], 8, sign)
	}
	if strings.Contains(s, ":") {
		return yaml11ClockInt(s, sign)
	}
	if len(s) > 1 && s[0] == '0' {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(s, "_", ""), 10, 64)
	return sign * n, err == nil
}

func yaml11ClockInt(s string, sign int64) (int64, bool) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(parts[0], "_", ""), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	for _, part := range parts[1:] {
		v, ok := yaml11ClockPart(part)
		if !ok {
			return 0, false
		}
		n = n*60 + v
	}
	return sign * n, true
}

func yaml11ClockPart(part string) (int64, bool) {
	if part == "" || len(part) > 2 {
		return 0, false
	}
	v, err := strconv.ParseInt(part, 10, 64)
	return v, err == nil && v >= 0 && v <= 59
}

func yaml11Float(raw string) (float64, bool) {
	s := strings.ReplaceAll(raw, "_", "")
	lower := strings.ToLower(strings.TrimPrefix(strings.TrimPrefix(s, "+"), "-"))
	if lower == ".inf" || lower == ".nan" {
		return 0, true
	}
	if strings.Contains(s, ":") {
		return yaml11ClockFloat(s)
	}
	if !strings.Contains(s, ".") {
		return 0, false
	}
	if i := strings.IndexAny(s, "eE"); i >= 0 &&
		(i+1 >= len(s) || (s[i+1] != '+' && s[i+1] != '-')) {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

func yaml11ClockFloat(s string) (float64, bool) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 {
		return 0, false
	}
	n, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	last := parts[len(parts)-1]
	if !strings.Contains(last, ".") {
		return 0, false
	}
	for _, part := range parts[1 : len(parts)-1] {
		v, ok := yaml11ClockPart(part)
		if !ok {
			return 0, false
		}
		n = n*60 + v
	}
	v, err := strconv.ParseFloat(last, 64)
	if err != nil || v < 0 || v >= 60 {
		return 0, false
	}
	return float64(n) + v, true
}

func yaml11Base(digits string, base int, sign int64) (int64, bool) {
	digits = strings.ReplaceAll(digits, "_", "")
	if digits == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(digits, base, 64)
	return sign * n, err == nil
}

func yaml11Timestamp(raw string) bool {
	for _, layout := range []string{"2006-01-02", time.RFC3339} {
		if _, err := time.Parse(layout, raw); err == nil {
			return true
		}
	}
	return false
}
