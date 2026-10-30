// Package toolset implements the declarative toolset composition of
// SPEC §12 / ADR-139: named groups of tool names, selected from config per
// platform, with custom groups defined in config.
//
// A toolset only chooses which tools are exposed to the model. It has no
// approval, effect, sandbox or policy field, and this package never imports
// internal/tools: composing a toolset cannot make a tool auto-approved.
//
// Some built-in members are declared before the tool exists in the binary
// (memory, session_search, the skills tools, web/vision generation). They are
// resolved as "unavailable" and reported to the caller, never dropped
// silently; the caller warns once and leaves them out of the request.
package toolset

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
)

// Errors of config validation and resolution.
var (
	ErrInvalid        = errors.New("toolset: invalid config")
	ErrUnknownToolset = errors.New("toolset: unknown toolset")
	ErrUnknownTool    = errors.New("toolset: unknown tool")
	ErrCycle          = errors.New("toolset: include cycle")
	ErrDuplicate      = errors.New("toolset: duplicate name")
)

// Definition is one toolset. The map key in [Config.Custom] names a custom
// one; built-ins carry Name directly.
type Definition struct {
	Name        string   `toml:"-"`
	Description string   `toml:"description"`
	Tools       []string `toml:"tools"`
	Includes    []string `toml:"includes"`
}

// Config is the [toolsets] section (SPEC §12, ADR-139):
//
//	[toolsets]
//	active = ["coding"]
//	disabled = ["terminal"]
//	[toolsets.platform.windows]
//	active = ["safe"]              # replaces both lists on windows
//	[toolsets.custom.review]
//	description = "review only"
//	tools = ["read_file", "grep"]
//	includes = ["web"]
//
// An empty Active (after the platform override) keeps the pre-toolset
// behaviour: every registered tool is exposed. A custom tool must name a
// declared tool (otherwise config validation fails); a declared tool the
// binary does not register resolves as unavailable, never silently dropped.
type Config struct {
	Active   []string              `toml:"active"`
	Disabled []string              `toml:"disabled"`
	Platform map[string]Override   `toml:"platform"`
	Custom   map[string]Definition `toml:"custom"`
}

// Override replaces both lists for one GOOS. Present means "use these lists".
type Override struct {
	Active   []string `toml:"active"`
	Disabled []string `toml:"disabled"`
}

// Selection is the resolved toolset set for one run.
type Selection struct {
	Active      []string        // effective active names after the platform override
	Tools       []string        // exposed registered tools, sorted
	Unavailable []string        // declared members the binary does not register
	Allowed     map[string]bool // nil = no filtering (legacy); membership = exposed
}

// Builtin returns the F2 toolset table. Names follow the Damn-Bot tool
// registry where it differs from Hermes (shell for terminal/process_manage,
// read_file/glob/grep for patch/search_files).
func Builtin() []Definition {
	file := []string{"read_file", "write_file", "edit_file", "glob", "grep"}
	web := []string{"web_search", "web_fetch"}
	skills := []string{"skills_list", "skill_view", "skill_manage"}
	core := slices.Concat(
		web, []string{"shell"}, file, skills,
		[]string{"todo", "clarify", "memory", "session_search", "vision_analyze", "image_generate", "cronjob_manage"},
	)
	coding := slices.DeleteFunc(slices.Clone(core), func(t string) bool {
		switch t {
		case "vision_analyze", "image_generate", "text_to_speech", "cronjob_manage":
			return true
		}
		return false
	})
	acp := slices.DeleteFunc(slices.Clone(coding), func(t string) bool { return t == "clarify" })
	defs := []Definition{
		{Name: "web", Description: "Web research and content extraction tools", Tools: web},
		{Name: "file", Description: "File manipulation tools: read, write, edit, glob, grep", Tools: file},
		{Name: "terminal", Description: "Terminal/command execution", Tools: []string{"shell"}},
		{Name: "todo", Description: "Task planning and tracking", Tools: []string{"todo"}},
		{Name: "clarify", Description: "Ask the user one clarifying question", Tools: []string{"clarify"}},
		{Name: "memory", Description: "Persistent memory across sessions", Tools: []string{"memory"}},
		{Name: "session_search", Description: "Search and recall past conversations", Tools: []string{"session_search"}},
		{Name: "skills", Description: "Access and manage skill documents", Tools: skills},
		{Name: "context_engine", Description: "Runtime tools exposed by the active context engine"}, // data-only slot
		{Name: "debugging", Description: "Debugging toolkit", Tools: []string{"shell"}, Includes: []string{"web", "file"}},
		{Name: "safe", Description: "Safe toolkit without terminal access", Tools: []string{"vision_analyze", "image_generate"}, Includes: []string{"web"}},
		{Name: "coding", Description: "Coding toolkit: files, shell, web, skills, todo, memory, session_search, clarify", Tools: coding},
		{Name: "hermes-acp", Description: "Coding toolkit for editor integration", Tools: acp},
		{Name: "cronjob", Description: "Scheduled job management", Tools: []string{"cronjob_manage"}},
		{Name: "hermes-cli", Description: "Full headless toolkit", Tools: core},
		{Name: "hermes-cron", Description: "Default cron toolset: the full headless toolkit", Includes: []string{"hermes-cli"}},
		{Name: "hermes-telegram", Description: "Telegram bot toolset: full access for personal use", Includes: []string{"hermes-cli"}},
		{Name: "hermes-gateway", Description: "Gateway toolset: union of the messaging platform toolsets", Includes: []string{"hermes-telegram"}},
		{Name: "cli", Description: "Alias of the full headless toolkit", Includes: []string{"hermes-cli"}},
	}
	out := make([]Definition, len(defs)) // fresh slices: no mutable package state
	for i := range defs {
		out[i] = defs[i]
		out[i].Tools = slices.Clone(defs[i].Tools)
		out[i].Includes = slices.Clone(defs[i].Includes)
	}
	return out
}

func builtinMap() map[string]Definition {
	defs := Builtin()
	m := make(map[string]Definition, len(defs))
	for _, d := range defs {
		m[d.Name] = d
	}
	return m
}

// DeclaredTools returns every tool name any built-in toolset mentions. A
// custom toolset may only reference one of these names: a typo is a config
// error, while a declared-but-unregistered name resolves as unavailable.
func DeclaredTools() map[string]bool {
	out := make(map[string]bool)
	for _, d := range Builtin() {
		for _, t := range d.Tools {
			out[t] = true
		}
	}
	return out
}

// Validate checks names, custom composition and cycles. It is pure.
func (c *Config) Validate() error {
	all, err := c.definitions()
	if err != nil {
		return err
	}
	if err := validateCustomTools(c.Custom); err != nil {
		return err
	}
	if err := validateLists(all, c.Active, c.Disabled); err != nil {
		return err
	}
	if err := validatePlatforms(all, c.Platform); err != nil {
		return err
	}
	return validateCycles(all)
}

// definitions merges the built-ins with the custom entries, rejecting empty
// names, built-in shadows and empty compositions.
func (c *Config) definitions() (map[string]Definition, error) {
	all := builtinMap()
	for name, d := range c.Custom {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("%w: empty custom name", ErrInvalid)
		}
		if _, dup := all[name]; dup {
			return nil, fmt.Errorf("%w: %q shadows a built-in toolset", ErrDuplicate, name)
		}
		if len(d.Tools) == 0 && len(d.Includes) == 0 {
			return nil, fmt.Errorf("%w: custom toolset %q is empty", ErrInvalid, name)
		}
	}
	maps.Copy(all, c.Custom)
	return all, nil
}

func validateCustomTools(custom map[string]Definition) error {
	declared := DeclaredTools()
	for name, d := range custom {
		for _, t := range d.Tools {
			if !declared[t] {
				return fmt.Errorf("%w: custom toolset %q: %w %q", ErrInvalid, name, ErrUnknownTool, t)
			}
		}
	}
	return nil
}

func validateLists(all map[string]Definition, active, disabled []string) error {
	for _, list := range [][]string{active, disabled} {
		for _, name := range list {
			if name == "*" || name == "all" {
				continue
			}
			if _, ok := all[name]; !ok {
				return fmt.Errorf("%w: %w %q", ErrInvalid, ErrUnknownToolset, name)
			}
		}
	}
	return nil
}

func validatePlatforms(all map[string]Definition, platforms map[string]Override) error {
	for name, ov := range platforms {
		switch name {
		case "linux", "darwin", "windows":
		default:
			return fmt.Errorf("%w: platform %q (want linux, darwin or windows)", ErrInvalid, name)
		}
		for _, n := range slices.Concat(ov.Active, ov.Disabled) {
			if n == "*" || n == "all" {
				continue
			}
			if _, ok := all[n]; !ok {
				return fmt.Errorf("%w: platform %s: %w %q", ErrInvalid, name, ErrUnknownToolset, n)
			}
		}
	}
	return nil
}

// validateCycles rejects recursive includes; built-ins are acyclic by
// construction, custom ones may nest.
func validateCycles(all map[string]Definition) error {
	state := make(map[string]uint8, len(all)) // 1 visiting, 2 done
	var visit func(string, []string) error
	visit = func(name string, path []string) error {
		switch state[name] {
		case 1:
			return fmt.Errorf("%w: %s", ErrCycle, strings.Join(append(path, name), " -> "))
		case 2:
			return nil
		}
		d, ok := all[name]
		if !ok {
			return fmt.Errorf("%w: %w %q", ErrInvalid, ErrUnknownToolset, name)
		}
		state[name] = 1
		for _, inc := range d.Includes {
			if err := visit(inc, append(path, name)); err != nil {
				return err
			}
		}
		state[name] = 2
		return nil
	}
	for name := range all {
		if err := visit(name, nil); err != nil {
			return err
		}
	}
	return nil
}

// Resolve turns cfg into the exposed tool set for one GOOS. available is the
// set of names the binary actually registered. An empty effective Active
// returns Allowed=nil (legacy: expose every registered tool).
func Resolve(cfg Config, platform string, available map[string]bool) (Selection, error) {
	if err := cfg.Validate(); err != nil {
		return Selection{}, err
	}
	all := builtinMap()
	maps.Copy(all, cfg.Custom)
	active, disabled := cfg.Active, cfg.Disabled
	if ov, ok := cfg.Platform[platform]; ok {
		active, disabled = ov.Active, ov.Disabled
	}
	chosen := make(map[string]bool)
	switch {
	case len(active) == 0 && len(disabled) == 0:
		tools := slices.Sorted(maps.Keys(available))
		return Selection{Tools: tools}, nil
	case len(active) == 0:
		for name := range available { // disabled-only: start from every exposed tool
			chosen[name] = true
		}
	default:
		active = expandAll(active, all)
		for _, name := range active {
			for _, t := range resolveOne(all, name) {
				chosen[t] = true
			}
		}
	}
	disabled = expandAll(disabled, all)
	for _, name := range disabled {
		for _, t := range resolveOne(all, name) {
			delete(chosen, t)
		}
	}
	sel := Selection{Active: slices.Clone(active), Allowed: make(map[string]bool)}
	for t := range chosen {
		if available[t] {
			sel.Tools = append(sel.Tools, t)
			sel.Allowed[t] = true
		} else {
			sel.Unavailable = append(sel.Unavailable, t)
		}
	}
	sort.Strings(sel.Tools)
	sort.Strings(sel.Unavailable)
	return sel, nil
}

// expandAll replaces the * / all sentinel with every name (sorted).
func expandAll(list []string, all map[string]Definition) []string {
	var out []string
	for _, name := range list {
		if name != "*" && name != "all" {
			out = append(out, name)
			continue
		}
		out = append(out, slices.Sorted(maps.Keys(all))...)
	}
	return out
}

// resolveOne returns the transitive members of name. Validate has already
// rejected cycles and unknown includes.
func resolveOne(all map[string]Definition, name string) []string {
	seen := make(map[string]bool)
	var walk func(string)
	walk = func(n string) {
		d, ok := all[n]
		if !ok {
			return
		}
		for _, inc := range d.Includes {
			walk(inc)
		}
		for _, t := range d.Tools {
			seen[t] = true
		}
	}
	walk(name)
	return slices.Sorted(maps.Keys(seen))
}
