package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/modules"
)

// configModules is the read-only F3 surface of SPEC §3.2.1/§10 (ADR-228).
// Descriptors come from the static registry; the factory is never called.
func configModules(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		if args[0] == "restart" {
			say(stderr, "config modules restart: serve/restart is F4\n")
			return 2
		}
		say(stderr, "config modules: unknown subcommand %q\n", cleanArg(args[0]))
		return 2
	}
	descs := modules.Default().Descriptors()
	if len(descs) == 0 {
		return say(stdout, "no modules registered in this build\n")
	}
	enabled, err := moduleEnabledMap()
	if err != nil {
		return configFail(stderr, "config modules", err)
	}
	slices.SortFunc(descs, func(a, b modules.Descriptor) int { return strings.Compare(a.ID, b.ID) })
	if code := say(stdout, "id\tgroup\tconfig\ttools\tenabled\n"); code != 0 {
		return code
	}
	for _, d := range descs {
		if code := say(stdout, "%s\t%s\t%s\t%d\t%t\n", d.ID, d.Group, d.ConfigKeyOrID(), len(d.Tools), enabled[d.ID]); code != 0 {
			return code
		}
	}
	return 0
}

// moduleEnabledMap reads [modules.<id>].enabled without the config engine:
// F3 refuses module sections (ADR-197), but the status view must still report
// a section that is present in the file.
func moduleEnabledMap() (map[string]bool, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return nil, fmt.Errorf("config deps: %w", err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return nil, fmt.Errorf("config paths: %w", err)
	}
	data, err := os.ReadFile(paths.ConfigFile())
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", paths.ConfigFile(), err)
	}
	var raw map[string]any
	if _, err := toml.Decode(string(data), &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", paths.ConfigFile(), err)
	}
	out := map[string]bool{}
	if mods, ok := raw["modules"].(map[string]any); ok {
		flattenModuleEnabled("", mods, out)
	}
	return out, nil
}

func flattenModuleEnabled(prefix string, table map[string]any, out map[string]bool) {
	for key, value := range table {
		id := key
		if prefix != "" {
			id = prefix + "." + key
		}
		child, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if enabled, ok := child["enabled"].(bool); ok {
			out[id] = enabled
		}
		flattenModuleEnabled(id, child, out)
	}
}
