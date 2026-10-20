package config

import (
	"path"
	"path/filepath"
	"strings"
)

// Paths are the per-profile directories derived from the OS conventions in
// SPEC §4.9 and §11. Both are absolute after [Loader.Paths].
type Paths struct {
	ConfigDir string
	DataDir   string
	goos      string // path convention used to build the strings
}

// ConfigFile returns <ConfigDir>/config.toml.
func (p Paths) ConfigFile() string { return joinOS(p.goos, p.ConfigDir, "config.toml") }

// Paths resolves the config and data directories for profile ("" = default
// profile). A non-empty profile must match [a-z0-9][a-z0-9_-]{0,63}; anything
// else is [ErrProfile], so a profile can never escape the data directory.
func (l *Loader) Paths(profile string) (Paths, error) {
	if profile != "" && !validProfile(profile) {
		return Paths{}, keyError(ErrProfile, "profile", "%q must match [a-z0-9][a-z0-9_-]{0,63}", profile)
	}
	cfgBase, dataBase, err := l.baseDirs()
	if err != nil {
		return Paths{}, err
	}
	if profile != "" {
		cfgBase = joinOS(l.deps.GOOS, cfgBase, "profiles", profile)
		dataBase = joinOS(l.deps.GOOS, dataBase, "profiles", profile)
	}
	return Paths{ConfigDir: cfgBase, DataDir: dataBase, goos: l.deps.GOOS}, nil
}

func (l *Loader) baseDirs() (configDir, dataDir string, err error) {
	home, err := l.deps.HomeDir()
	if err != nil {
		return "", "", &Error{Kind: ErrHome, Cause: err, Msg: "cannot resolve home directory"}
	}
	j := func(elem ...string) string { return joinOS(l.deps.GOOS, elem...) }
	switch l.deps.GOOS {
	case "windows":
		appData := l.deps.Getenv("APPDATA")
		if appData == "" {
			appData = j(home, "AppData", "Roaming")
		}
		local := l.deps.Getenv("LOCALAPPDATA")
		if local == "" {
			local = j(home, "AppData", "Local")
		}
		return j(appData, "damnbot"), j(local, "damnbot"), nil
	case "darwin":
		base := j(home, "Library", "Application Support", "damnbot")
		return base, base, nil
	default:
		cfg := absoluteXDG(l.deps.GOOS, l.deps.Getenv("XDG_CONFIG_HOME"), j(home, ".config"))
		data := absoluteXDG(l.deps.GOOS, l.deps.Getenv("XDG_DATA_HOME"), j(home, ".local", "share"))
		return j(cfg, "damnbot"), j(data, "damnbot"), nil
	}
}

// absoluteXDG ignores a relative XDG value: the XDG base directory spec says
// implementations must ignore relative paths.
func absoluteXDG(goos, value, fallback string) string {
	if value != "" && absOS(goos, value) {
		return value
	}
	return fallback
}

// joinOS uses the target OS's separator, not the host's: tests exercise all
// three path conventions on one machine.
func joinOS(goos string, elem ...string) string {
	if goos == "windows" {
		return filepath.Join(elem...)
	}
	return path.Join(elem...)
}

func absOS(goos, p string) bool {
	if goos == "windows" {
		return filepath.IsAbs(p)
	}
	return path.IsAbs(p)
}

func validProfile(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && (c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// DenyPaths returns the deny_paths patterns with <config_dir> replaced by the
// profile's config directory (SPEC §12; ADR-097 expects the caller to expand
// the placeholder). The returned slice is a copy.
func (c *Config) DenyPaths(configDir string) []string {
	out := make([]string, len(c.Approval.DenyPaths))
	for i, p := range c.Approval.DenyPaths {
		out[i] = strings.ReplaceAll(p, "<config_dir>", configDir)
	}
	return out
}
