package importer

import (
	"path/filepath"
	"strconv"
	"strings"
)

func (i *Importer) collectSecrets(root string, c *collected) error {
	raw, err := i.readOptional(filepath.Join(root, ".env"))
	if err != nil {
		return err
	}
	if raw == nil {
		return nil
	}
	referenced := referencedNames(c)
	seen := map[string]struct{}{}
	for number, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, value, ok := parseEnvLine(line)
		if !ok {
			c.report.skip(SourceSecrets, secretLineKey(number), "line is not a KEY=VALUE assignment")
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			c.report.skip(SourceSecrets, key, "duplicate key in .env")
			continue
		}
		seen[key] = struct{}{}
		if key == "SUDO_PASSWORD" {
			// GAP-SEC-14: a privileged password is never migrated.
			c.report.skip(SourceSecrets, key, "refused: privilege escalation secret (GAP-SEC-14)")
			continue
		}
		if c.reject(SourceSecrets, key) {
			continue
		}
		if _, used := referenced[key]; !used {
			c.report.skip(SourceSecrets, key, "not referenced by the imported config")
			continue
		}
		c.queueSecret(key, value)
	}
	return nil
}

func secretLineKey(number int) string {
	return "line " + strconv.Itoa(number+1)
}

func parseEnvLine(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "export ")
	key, value, found := strings.Cut(line, "=")
	if !found {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	if !validEnvName(key) {
		return "", "", false
	}
	value = strings.TrimSpace(value)
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
		value = value[1 : len(value)-1]
	}
	if value == "" {
		return "", "", false
	}
	return key, value, true
}

// referencedNames is the set of env/secret names the imported items actually
// use: ${NAME} inside imported config values and keychain references produced
// by the MCP mapper.
func referencedNames(c *collected) map[string]struct{} {
	out := map[string]struct{}{}
	for _, entry := range c.entries {
		addRefs(out, entry.Value)
	}
	for _, server := range c.servers {
		for _, value := range server.Env {
			addKeychainRef(out, value)
		}
		for _, value := range server.Headers {
			addKeychainRef(out, value)
		}
	}
	return out
}

func addRefs(out map[string]struct{}, value any) {
	switch typed := value.(type) {
	case string:
		for _, name := range scanRefs(typed) {
			out[name] = struct{}{}
		}
	case []string:
		for _, item := range typed {
			for _, name := range scanRefs(item) {
				out[name] = struct{}{}
			}
		}
	}
}

func addKeychainRef(out map[string]struct{}, value string) {
	if name, ok := strings.CutPrefix(value, "keychain:"); ok && validEnvName(name) {
		out[name] = struct{}{}
	}
}

func scanRefs(text string) []string {
	var names []string
	for {
		start := strings.Index(text, "${")
		if start < 0 {
			return names
		}
		end := strings.IndexByte(text[start:], '}')
		if end < 0 {
			return names
		}
		name := text[start+2 : start+end]
		if validEnvName(name) {
			names = append(names, name)
		}
		text = text[start+end+1:]
	}
}
