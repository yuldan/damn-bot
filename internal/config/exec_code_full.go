//go:build !minimal

package config

func validateExecCode(c *Config) error { return validCodeLangs(c.Exec.CodeAutoLanguages) }

// validCodeLangs validates exec.code_auto_languages: closed set, no shell
// languages, no duplicates (ADR-089).
func validCodeLangs(langs []string) error {
	seen := map[string]bool{}
	for _, lang := range langs {
		switch lang {
		case "python", "javascript", "typescript", "go", "starlark":
		case "bash", "sh", "zsh", "pwsh", "powershell", "cmd":
			return invalid("exec.code_auto_languages", "%q is a shell language; use shell_allow", lang)
		default:
			return invalid("exec.code_auto_languages", "%q is not a supported language", lang)
		}
		if seen[lang] {
			return invalid("exec.code_auto_languages", "duplicate %q", lang)
		}
		seen[lang] = true
	}
	return nil
}
