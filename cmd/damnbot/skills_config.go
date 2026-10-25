package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"

	"github.com/damnbot/damnbot/internal/config"
)

// skillsConfig prints the effective [skills] configuration and the config/data
// directories the CLI resolved. Split out of skills_cli.go to stay under the
// 800-line file limit (E-14/E-71).
func skillsConfig(args []string, stdout, stderr io.Writer) int {
	a, err := parseSkillArgs(args)
	if err != nil || len(a.pos) != 0 {
		if err != nil {
			say(stderr, "skills config: %v\n", err)
		}
		skillsUsage(stderr)
		return 2
	}
	loader, err := config.New(config.OS())
	if err != nil {
		say(stderr, "skills config: %v\n", err)
		return 1
	}
	cfg := config.Default()
	cfgDir, _, perr := skillsPaths()
	if perr != nil {
		say(stderr, "skills config: %v\n", perr)
		return 1
	}
	if _, serr := os.Stat(filepath.Join(cfgDir, "config.toml")); serr == nil {
		loaded, lerr := loader.LoadProfile("")
		if lerr != nil {
			say(stderr, "skills config: %v\n", lerr)
			return 1
		}
		cfg = loaded
	}
	b, err := json.MarshalIndent(cfg.Skills, "", "  ")
	if err != nil {
		say(stderr, "skills config: %v\n", err)
		return 1
	}
	say(stdout, "%s\n", b)
	return 0
}
