package main

import (
	"github.com/BurntSushi/toml"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/i18n"
)

// languageFile is the only slice of config.toml the CLI reads before a full
// load. [ui] language selects the catalog for help, config errors, and the
// web API auth message; the rest of the file is validated by config.Load.
type languageFile struct {
	UI struct {
		Language string `toml:"language"`
	} `toml:"ui"`
}

// localeFromFile resolves [ui].language best-effort. A missing or invalid
// file, or an auto/unknown value, falls back to English: a broken config must
// never take the help screen down with it.
func localeFromFile(path string) string {
	var file languageFile
	if _, err := toml.DecodeFile(path, &file); err != nil {
		return i18n.Default
	}
	return i18n.Resolve(file.UI.Language)
}

// currentLocale resolves the active profile's config file and reads its UI
// language; any resolution error falls back to English.
func currentLocale() string {
	loader, err := config.New(config.OS())
	if err != nil {
		return i18n.Default
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return i18n.Default
	}
	return localeFromFile(paths.ConfigFile())
}
