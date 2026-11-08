//go:build minimal

package i18n

import "embed"

// catalogFS is the minimal build's catalog: English only, so the size gate
// keeps its headroom. Every other locale resolves to English through
// [Resolve]/[T]; the full build embeds all eighteen (catalog_full.go).
//
//go:embed locales/en.toml
var catalogFS embed.FS

// requiredLocales is the minimal-build locale set.
func requiredLocales() []string { return []string{"en"} }
