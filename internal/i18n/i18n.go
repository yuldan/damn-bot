// Package i18n holds the embedded UI string catalog of SPEC §9.12: eighteen
// BCP-47 locales (the seventeen hermes parity languages plus Indonesian, Q16)
// with English as the fallback. Catalogs are TOML files compiled into the
// binary ([//go:embed]) and read on demand; [Validate] and the named tests
// enforce the whole-set invariant. The parser is the repository's existing
// BurntSushi/toml dependency (internal/config/load.go:12).
//
// The package never panics: a broken catalog is a test failure ([Validate],
// TestI18nNoMissingKeys) and an unknown key falls back to the key itself.
package i18n

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// Default is the fallback locale; every catalog key is guaranteed to exist in
// it.
const Default = "en"

// Auto is the config value meaning "follow the user's environment". The CLI
// resolves it to [Default].
const Auto = "auto"

// catalog is the parsed form: locale code → message key → text.
type catalog map[string]map[string]string

// Locales returns the registered locale codes in stable (lexical) order.
func Locales() []string {
	locales := slices.Clone(requiredLocales())
	slices.Sort(locales)
	return locales
}

// Resolve maps a config language value to a registered locale: "auto" and
// unknown values fall back to [Default], case is ignored, and a regional tag
// such as "pt-BR" resolves to its registered primary subtag ("pt").
func Resolve(locale string) string {
	tag := strings.ToLower(strings.TrimSpace(locale))
	if tag == "" || tag == Auto {
		return Default
	}
	if slices.Contains(requiredLocales(), tag) {
		return tag
	}
	if base, _, ok := strings.Cut(tag, "-"); ok && slices.Contains(requiredLocales(), base) {
		return base
	}
	return Default
}

// T returns key's text in locale, falling back to [Default] and then to key.
// With args it applies fmt.Sprintf to the translated string.
//
// The embedded catalogs are small and T is on low-frequency surfaces (help,
// config errors, auth messages), so it reads only the requested locale and
// the English fallback; the full-set invariant is enforced by [Validate] in
// tests rather than by a process-wide cache (E-10 keeps the global allowlist
// full).
func T(locale, key string, args ...any) string {
	text := lookup(locale, key)
	if len(args) == 0 || text == key {
		return text
	}
	return fmt.Sprintf(text, args...)
}

// Keys returns every catalog key in stable (lexical) order, or nil when the
// embedded catalog is invalid (see [Validate]).
func Keys() []string {
	catalogs, err := parseCatalogs()
	if err != nil {
		return nil
	}
	keys := slices.Collect(maps.Keys(catalogs[Default]))
	slices.Sort(keys)
	return keys
}

// Validate parses the embedded catalogs and checks the SPEC §9.12 invariant:
// every locale has exactly the keys of [Default], non-empty values, and the
// same printf directives. It is the named test hook for the embedded data.
func Validate() error {
	_, err := parseCatalogs()
	return err
}

// lookup resolves locale's text for key through the embedded catalogs,
// falling back to [Default] and then to the key.
func lookup(locale, key string) string {
	resolved := Resolve(locale)
	catalogs := make(catalog, 2)
	if messages, err := catalogFor(Default); err == nil {
		catalogs[Default] = messages
	}
	if resolved != Default {
		if messages, err := catalogFor(resolved); err == nil {
			catalogs[resolved] = messages
		}
	}
	return message(catalogs, resolved, key)
}

// message is the pure fallback chain over a parsed catalog set.
func message(catalogs catalog, locale, key string) string {
	if messages, ok := catalogs[Resolve(locale)]; ok {
		if text, ok := messages[key]; ok && text != "" {
			return text
		}
	}
	if text, ok := catalogs[Default][key]; ok && text != "" {
		return text
	}
	return key
}

// catalogFor reads and parses one embedded locale file.
func catalogFor(locale string) (map[string]string, error) {
	name := "locales/" + locale + ".toml"
	data, err := catalogFS.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("i18n: %s: %w", name, err)
	}
	messages := map[string]string{}
	if err := toml.Unmarshal(data, &messages); err != nil {
		return nil, fmt.Errorf("i18n: %s: %w", name, err)
	}
	return messages, nil
}

// parseCatalogs reads every embedded locale file and validates the set.
func parseCatalogs() (catalog, error) {
	files, err := fs.Glob(catalogFS, "locales/*.toml")
	if err != nil {
		return nil, fmt.Errorf("i18n: catalog glob: %w", err)
	}
	locales := requiredLocales()
	if len(files) != len(locales) {
		return nil, fmt.Errorf("i18n: %d embedded catalogs, want %d", len(files), len(locales))
	}
	catalogs := make(catalog, len(locales))
	for _, locale := range locales {
		messages, err := catalogFor(locale)
		if err != nil {
			return nil, err
		}
		if len(messages) == 0 {
			return nil, fmt.Errorf("i18n: %s: empty catalog", locale)
		}
		catalogs[locale] = messages
	}
	if err := validateCatalogs(catalogs, locales); err != nil {
		return nil, err
	}
	return catalogs, nil
}

// validateCatalogs enforces the key and format invariant on a parsed catalog
// set. locales is the expected code list, in any order.
func validateCatalogs(catalogs catalog, locales []string) error {
	base, ok := catalogs[Default]
	if !ok || len(base) == 0 {
		return fmt.Errorf("i18n: missing or empty %q catalog", Default)
	}
	var errs []error
	for _, locale := range locales {
		messages, ok := catalogs[locale]
		if !ok {
			errs = append(errs, fmt.Errorf("i18n: missing %q catalog", locale))
			continue
		}
		if len(messages) != len(base) {
			errs = append(errs, fmt.Errorf("i18n: %s has %d keys, want %d", locale, len(messages), len(base)))
		}
		for key, baseText := range base {
			text, ok := messages[key]
			switch {
			case !ok:
				errs = append(errs, fmt.Errorf("i18n: %s: missing key %q", locale, key))
			case strings.TrimSpace(text) == "":
				errs = append(errs, fmt.Errorf("i18n: %s: empty value for %q", locale, key))
			case !maps.Equal(formatVerbs(text), formatVerbs(baseText)):
				errs = append(errs, fmt.Errorf("i18n: %s: printf directives differ for %q", locale, key))
			}
		}
		for key := range messages {
			if _, ok := base[key]; !ok {
				errs = append(errs, fmt.Errorf("i18n: %s: unknown key %q", locale, key))
			}
		}
	}
	for locale := range catalogs {
		if !slices.Contains(locales, locale) {
			errs = append(errs, fmt.Errorf("i18n: unexpected locale %q", locale))
		}
	}
	return errors.Join(errs...)
}

// formatVerbs counts printf directives by verb letter, ignoring "%%".
func formatVerbs(text string) map[byte]int {
	verbs := map[byte]int{}
	escaped := false
	for i := range len(text) {
		switch {
		case escaped:
			escaped = false
		case text[i] != '%':
			continue
		case i+1 < len(text) && text[i+1] == '%':
			escaped = true
		case i+1 < len(text):
			verbs[text[i+1]]++
		}
	}
	return verbs
}
