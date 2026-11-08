//go:build !minimal

package i18n

import "embed"

// catalogFS is the full embedded catalog: one TOML file per SPEC §9.12
// locale. The size-check gate excludes it from the minimal build
// (catalog_minimal.go).
//
//go:embed locales/*.toml
var catalogFS embed.FS

// requiredLocales is the SPEC §9.12 set: the seventeen hermes parity
// languages plus Indonesian (Q16), in the stable lexical order of [Locales].
func requiredLocales() []string {
	return []string{
		"af", "ar", "de", "en", "es", "fr", "ga", "hu", "id", "it",
		"ja", "ko", "pt", "ru", "tr", "uk", "zh", "zh-hant",
	}
}
