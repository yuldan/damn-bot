//go:build minimal

package config

// defaultReplyLanguage is empty in the minimal build: reply_language is a
// full-build surface (GAP-INF-09), so no validation strings are linked.
const defaultReplyLanguage = ""

func validateReplyLanguage(string) error { return nil }
