//go:build !minimal

package assemble

// replyLine renders the stable reply-language instruction for a resolved
// code, or "" for auto (GAP-INF-09).
func replyLine(code string) string {
	if code == "" {
		return ""
	}
	return "Reply in " + languageName(code) + " (" + code + ")."
}

func languageName(code string) string {
	names := map[string]string{
		"af": "Afrikaans", "ar": "Arabic", "de": "German", "en": "English",
		"es": "Spanish", "fr": "French", "ga": "Irish", "hu": "Hungarian",
		"id": "Indonesian", "it": "Italian", "ja": "Japanese", "ko": "Korean",
		"pt": "Portuguese", "ru": "Russian", "tr": "Turkish", "uk": "Ukrainian",
		"zh": "Chinese", "zh-hant": "Chinese (Traditional)",
	}
	if name, ok := names[code]; ok {
		return name
	}
	return code
}
