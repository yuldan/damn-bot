package importer

import (
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
)

func (i *Importer) collectMCP(c *collected) error {
	if c.configDoc == nil {
		return nil
	}
	raw, ok := c.configDoc["mcp_servers"]
	if !ok {
		return nil
	}
	servers, ok := raw.(map[string]any)
	if !ok {
		return syntaxError("config.yaml", errors.New("mcp_servers must be a table"))
	}
	for _, name := range slices.Sorted(maps.Keys(servers)) {
		if c.reject(SourceMCP, name) {
			continue
		}
		fields, ok := servers[name].(map[string]any)
		if !ok {
			c.report.skip(SourceMCP, name, "server definition is not a table")
			continue
		}
		server := MCPServer{
			Name:     name,
			Command:  childString(fields, "command"),
			URL:      childString(fields, "url"),
			Args:     childList(fields, "args"),
			TimeoutS: 120,
		}
		if (server.Command == "") == (server.URL == "") {
			c.report.skip(SourceMCP, name, "must set exactly one of command or url")
			continue
		}
		if timeout, ok := childInt(fields, "timeout"); ok && timeout > 0 {
			server.TimeoutS = timeout
		}
		server.Env = i.importPairs(name, "env", fields["env"], c)
		server.Headers = i.importPairs(name, "headers", fields["headers"], c)
		c.servers = append(c.servers, server)
		c.report.item(SourceMCP)
	}
	return nil
}

// importPairs copies env/header pairs, replacing every secret with a keychain
// reference. Non-string values are not part of a Hermes definition and are
// omitted; the server item itself is still accounted for.
func (i *Importer) importPairs(server, kind string, raw any, c *collected) map[string]string {
	fields, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(fields))
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		value, ok := fields[key].(string)
		if !ok {
			continue
		}
		out[key] = secretize(c, server, kind+"."+key, value)
	}
	return out
}

func secretize(c *collected, server, key, value string) string {
	if strings.HasPrefix(value, "keychain:") {
		return value
	}
	if name, ok := envRef(value); ok {
		return "keychain:" + name
	}
	if isSensitiveName(key) || looksSecret(value) {
		name := secretName("mcp." + server + "." + key)
		c.queueSecret(name, value)
		return "keychain:" + name
	}
	return value
}

func isSensitiveName(name string) bool {
	lower := strings.ToLower(name)
	for _, marker := range []string{"token", "api-key", "api_key", "apikey", "secret", "password", "authorization"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func looksSecret(value string) bool {
	for _, prefix := range []string{"sk-", "ghp_", "github_pat_", "xoxb-", "xoxp-", "Bearer "} {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func envRef(value string) (string, bool) {
	if !strings.HasPrefix(value, "${") || !strings.HasSuffix(value, "}") {
		return "", false
	}
	name := value[2 : len(value)-1]
	if name == "" || !validEnvName(name) {
		return "", false
	}
	return name, true
}

func validEnvName(name string) bool {
	for i := range len(name) {
		c := name[i]
		switch {
		case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return name != ""
}

func childString(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

func childList(m map[string]any, key string) []string {
	items, ok := m[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func childInt(m map[string]any, key string) (int, bool) {
	switch v := m[key].(type) {
	case int:
		return v, true
	case string:
		n, err := strconv.Atoi(v)
		return n, err == nil
	default:
		return 0, false
	}
}
