package importer

import (
	"fmt"
	"strings"
)

// runtimeRefs lists external runtimes named by a skill body. The names are
// taken from the Hermes skill corpus (python3/python, node/npm, ffmpeg,
// docker), not an open-ended guess; they become host requirements on import.
func runtimeRefs(body string) []string {
	lower := strings.ToLower(body)
	var out []string
	if commandRef(lower, "python3") || commandRef(lower, "python") {
		out = append(out, "python3")
	}
	if commandRef(lower, "node") || commandRef(lower, "npm") {
		out = append(out, "node")
	}
	for _, name := range []string{"ffmpeg", "docker"} {
		if commandRef(lower, name) {
			out = append(out, name)
		}
	}
	return out
}

func commandRef(text, name string) bool {
	for i := 0; ; {
		j := strings.Index(text[i:], name)
		if j < 0 {
			return false
		}
		j += i
		before := j == 0 || !wordByte(text[j-1])
		after := j+len(name) == len(text) || !wordByte(text[j+len(name)])
		if before && after {
			return true
		}
		i = j + len(name)
	}
}

func wordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// warnMissingRuntimes records one non-fatal warning per imported skill and
// missing runtime; it never rejects the skill, so the catalog can show the
// unmet requirement instead of losing it.
func (c *collected) warnMissingRuntimes(look func(string) (string, error)) {
	if look == nil {
		return
	}
	for i := range c.skills {
		sk := &c.skills[i]
		for _, name := range sk.Runtime {
			if _, err := look(name); err != nil {
				c.report.Warnings = append(c.report.Warnings,
					fmt.Sprintf("skill %q requires runtime %q (not found)", sk.Name, name))
			}
		}
	}
}
