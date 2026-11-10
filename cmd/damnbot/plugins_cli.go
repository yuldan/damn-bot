//go:build !minimal

package main

import (
	"flag"
	"fmt"
	"io"
	"sort"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/modules"
	plugcatalog "github.com/damnbot/damnbot/internal/plugins/catalog"
	"github.com/damnbot/damnbot/internal/plugins/resolve"
	skillcatalog "github.com/damnbot/damnbot/internal/skills/catalog"
)

// runPlugins is the `damnbot plugins` surface (SPEC §10, Q18/PLUG-009): a
// read-only view of the embedded 645-entry catalog. It never installs,
// downloads, starts or executes an entry.
func runPlugins(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		pluginsUsage(stderr)
		return 2
	}
	switch args[0] {
	case "list":
		return pluginsList(args[1:], stdout, stderr)
	case "show":
		return pluginsShow(args[1:], stdout, stderr)
	case "verify":
		return pluginsVerify(args[1:], stdout, stderr)
	default:
		say(stderr, "plugins: subcommand %q tidak dikenal\n", cleanArg(args[0]))
		pluginsUsage(stderr)
		return 2
	}
}

func pluginsUsage(w io.Writer) {
	say(w, "pemakaian: damnbot plugins list [--config <path>]\n")
	say(w, "           damnbot plugins show <id> [--config <path>]\n")
	say(w, "           damnbot plugins verify\n")
}

// pluginsFlags parses the shared --config flag and returns positionals.
func pluginsFlags(name string, args []string, stderr io.Writer) ([]string, string, int) {
	fs := flag.NewFlagSet("damnbot plugins "+name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config file whose registries resolve targets")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return nil, "", 2
	}
	return pos, *cfgPath, 0
}

// pluginSources reads the live registries resolution is allowed to see: the
// MCP servers of the loaded config, the 211-entry skills catalog, and the
// compiled-in module registry. No secret value is read or returned.
func pluginSources(cfgPath string) (resolve.Sources, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return resolve.Sources{}, fmt.Errorf("config deps: %w", err)
	}
	cfg, err := loadRunConfig(loader, cfgPath)
	if err != nil {
		return resolve.Sources{}, err
	}
	src := resolve.Sources{
		MCP:     make(map[string][]string, len(cfg.Modules.MCP.Servers)),
		Skills:  map[string]struct{}{},
		Modules: map[string]struct{}{},
	}
	for name, server := range cfg.Modules.MCP.Servers {
		tools := make([]string, 0, len(server.Tools))
		for tool := range server.Tools {
			tools = append(tools, tool)
		}
		sort.Strings(tools)
		src.MCP[name] = tools
	}
	skills, err := skillcatalog.Load()
	if err != nil {
		return resolve.Sources{}, fmt.Errorf("skills catalog: %w", err)
	}
	for _, e := range skills.Entries() {
		src.Skills[e.Name] = struct{}{}
	}
	for _, d := range modules.Default().Descriptors() {
		src.Modules[d.ID] = struct{}{}
	}
	return src, nil
}

// pluginStatus resolves one entry against src and returns the printed status.
func pluginStatus(c *plugcatalog.Catalog, src resolve.Sources, e *plugcatalog.Entry) string {
	if _, err := resolve.Resolve(c, src, e.ID); err != nil {
		return resolve.ErrModuleUnavailable.Error()
	}
	return "available"
}

func pluginsList(args []string, stdout, stderr io.Writer) int {
	rest, cfgPath, code := pluginsFlags("list", args, stderr)
	if code != 0 {
		return code
	}
	if len(rest) != 0 {
		say(stderr, "pemakaian: damnbot plugins list [--config <path>]\n")
		return 2
	}
	cat, src, code := loadPluginView(cfgPath, stderr)
	if code != 0 {
		return code
	}
	for _, e := range cat.Entries() {
		status := pluginStatus(cat, src, &e)
		if say(stdout, "%s\t%s\t%s\t%s\n", e.ID, e.Kind, e.Category, status) != 0 {
			return 1
		}
	}
	return 0
}

func pluginsShow(args []string, stdout, stderr io.Writer) int {
	rest, cfgPath, code := pluginsFlags("show", args, stderr)
	if code != 0 {
		return code
	}
	if len(rest) != 1 {
		say(stderr, "pemakaian: damnbot plugins show <id> [--config <path>]\n")
		return 2
	}
	cat, src, code := loadPluginView(cfgPath, stderr)
	if code != 0 {
		return code
	}
	e, ok := cat.Lookup(rest[0])
	if !ok {
		say(stderr, "plugins: unknown id %s\n", cleanArg(rest[0]))
		return 2
	}
	status := pluginStatus(cat, src, &e)
	target := "-"
	if status == "available" {
		target = fmt.Sprintf("%s %s", e.Kind, e.ID)
	}
	lines := []struct{ key, val string }{
		{"id", e.ID},
		{"kind", string(e.Kind)},
		{"category", e.Category},
		{"pin", e.Pin},
		{"digest", e.SHA256},
		{"target", target},
		{"status", status},
		{"description", e.Description},
	}
	for _, l := range lines {
		if say(stdout, "%s: %s\n", l.key, l.val) != 0 {
			return 1
		}
	}
	return 0
}

func pluginsVerify(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		say(stderr, "pemakaian: damnbot plugins verify\n")
		return 2
	}
	cat, err := plugcatalog.Load()
	if err != nil {
		say(stderr, "plugins verify: %s\n", cleanLine(err.Error()))
		return 1
	}
	kinds := map[plugcatalog.Kind]int{}
	for _, e := range cat.Entries() {
		kinds[e.Kind]++
	}
	return say(stdout, "ok: %d entries (mcp %d, skill %d, module %d)\n",
		cat.Count(), kinds[plugcatalog.KindMCP], kinds[plugcatalog.KindSkill], kinds[plugcatalog.KindModule])
}

// loadPluginView loads the embedded catalog and the live registries.
func loadPluginView(cfgPath string, stderr io.Writer) (*plugcatalog.Catalog, resolve.Sources, int) {
	cat, err := plugcatalog.Load()
	if err != nil {
		say(stderr, "plugins: %s\n", cleanLine(err.Error()))
		return nil, resolve.Sources{}, 1
	}
	src, err := pluginSources(cfgPath)
	if err != nil {
		say(stderr, "plugins: %s\n", cleanLine(err.Error()))
		return nil, resolve.Sources{}, 1
	}
	return cat, src, 0
}
