//go:build !minimal

package config

// defaultReplyLanguage is the SPEC default for `[ui] reply_language`.
const defaultReplyLanguage = "auto"

// validateReplyLanguage accepts auto, follow_ui or a §9.12 language code.
func validateReplyLanguage(v string) error {
	switch v {
	case "auto", "follow_ui":
		return nil
	default:
		if !validLanguage(v) {
			return invalid("ui.reply_language", "%q is not auto, follow_ui or a §9.12 language code", v)
		}
		return nil
	}
}
