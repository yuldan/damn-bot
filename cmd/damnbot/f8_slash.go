//go:build !minimal

package main

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/auto"
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/tui"
)

// f8SlashAdapter is the one implementation behind both the gateway and TUI
// F8 slash seams. It resolves the data dir on every call so a long-lived
// gateway follows the profile it was started with.
type f8SlashAdapter struct{ dir string }

func newGatewayF8(dir string) gateway.F8Commands { return &f8SlashAdapter{dir: dir} }
func newTUIF8(dir string) tui.SlashF8            { return &f8SlashAdapter{dir: dir} }

// F8Gateway implements gateway.F8Commands. Mutations are owner-only unless
// the handler says otherwise.
func (a *f8SlashAdapter) F8Gateway(ctx context.Context, role gateway.Role, sessionID, name string, args []string) string {
	return a.handle(ctx, role == gateway.RoleOwner, sessionID, name, args)
}

// F8TUI implements tui.SlashF8. The local TUI is the owner.
func (a *f8SlashAdapter) F8TUI(ctx context.Context, sessionID, name string, args []string) string {
	return a.handle(ctx, true, sessionID, name, args)
}

func canonicalF8(name string) string {
	switch name {
	case "suggest":
		return "suggestions"
	case "bp":
		return "blueprint"
	case "hb":
		return "heartbeat"
	case "proactive":
		return "loop"
	case "learning", "memory-graph":
		return "journey"
	case "generate-pet":
		return "hatch"
	default:
		return name
	}
}

func (a *f8SlashAdapter) handle(ctx context.Context, owner bool, sessionID, name string, args []string) string {
	dir, err := f8Dir(a.dir)
	if err != nil {
		return "f8: " + cleanLine(err.Error())
	}
	cmd := canonicalF8(name)
	switch cmd {
	case "suggestions":
		return a.suggestions(dir, owner, args)
	case "blueprint":
		return f8Blueprint(args)
	case "goal":
		return a.goal(dir, owner, sessionID, args)
	case "subgoal":
		return a.subgoal(dir, owner, sessionID, args)
	case "heartbeat":
		return a.heartbeat(dir, owner, sessionID, args)
	case "loop":
		return a.loop(dir, owner, sessionID, args)
	case "personality":
		return a.personality(dir, owner, args)
	case "insights":
		return f8SlashInsights(ctx, dir, args)
	case "journey":
		return a.journeySlash(dir, owner, args)
	case "pet":
		return a.petSlash(dir, owner, args)
	case "curator":
		return a.curatorSlash(dir, owner, args)
	case "hatch":
		return a.hatchSlash(dir, owner, args)
	case "debug":
		return a.debugSlash(dir, owner, args)
	default:
		return "unknown command /" + name
	}
}

func f8OwnerOnly(owner bool, name string) string {
	if owner {
		return ""
	}
	return "owner-only: /" + name + " changes state"
}

// f8SlashState is one JSON object store keyed by session id.
type f8SlashState[T any] struct {
	Sessions map[string]T `json:"sessions"`
}

func f8LoadSlash[T any](path string) (f8SlashState[T], error) {
	st := f8SlashState[T]{}
	if err := f8Load(path, &st); err != nil {
		return st, err
	}
	if st.Sessions == nil {
		st.Sessions = map[string]T{}
	}
	return st, nil
}

func f8SaveSlash[T any](path string, st *f8SlashState[T]) error {
	return f8Save(path, st)
}

// f8Suggestion is one stored suggestion.
type f8Suggestion struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Status string `json:"status"`
}

type f8Suggestions struct {
	Items []f8Suggestion `json:"items"`
}

func (a *f8SlashAdapter) suggestions(dir string, owner bool, args []string) string {
	path := f8StorePath(dir, "suggestions.json")
	st := f8Suggestions{}
	if err := f8Load(path, &st); err != nil {
		return "suggestions: " + cleanLine(err.Error())
	}
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "list", "catalog":
		return f8SuggestionCatalog(dir, st, sub == "catalog")
	case "accept", "dismiss":
		return f8SuggestionMutate(dir, owner, args, sub)
	case "clear":
		return f8SuggestionClear(dir, owner, args)
	default:
		return "usage: /suggestions [list|accept <id>|dismiss <id>|catalog|clear]"
	}
}

// f8SuggestionMutate accepts or dismisses one stored suggestion.
func f8SuggestionMutate(dir string, owner bool, args []string, sub string) string {
	if msg := f8OwnerOnly(owner, "suggestions"); msg != "" {
		return msg
	}
	if len(args) != 2 {
		return "usage: /suggestions " + sub + " <id>"
	}
	path := f8StorePath(dir, "suggestions.json")
	st := f8Suggestions{}
	if err := f8Load(path, &st); err != nil {
		return "suggestions: " + cleanLine(err.Error())
	}
	status := "accepted"
	if sub == "dismiss" {
		status = "dismissed"
	}
	for i := range st.Items {
		if st.Items[i].ID != args[1] {
			continue
		}
		st.Items[i].Status = status
		if err := f8Save(path, &st); err != nil {
			return "suggestions: " + cleanLine(err.Error())
		}
		return "suggestion " + args[1] + " " + status
	}
	return "suggestion " + args[1] + " not found"
}

// f8SuggestionClear removes every stored suggestion.
func f8SuggestionClear(dir string, owner bool, args []string) string {
	if msg := f8OwnerOnly(owner, "suggestions"); msg != "" {
		return msg
	}
	if len(args) != 1 {
		return "usage: /suggestions clear"
	}
	path := f8StorePath(dir, "suggestions.json")
	st := f8Suggestions{}
	if err := f8Load(path, &st); err != nil {
		return "suggestions: " + cleanLine(err.Error())
	}
	st.Items = nil
	if err := f8Save(path, &st); err != nil {
		return "suggestions: " + cleanLine(err.Error())
	}
	return "suggestions cleared"
}

// f8SuggestionCatalog reports the feature-gate state, the stored suggestions,
// and (for catalog) the closed AUTO feature list.
func f8SuggestionCatalog(dir string, st f8Suggestions, catalog bool) string {
	enabled := false
	if cfg, _, _, err := f8LoadSetup("", dir); err == nil {
		enabled = cfg.Modules.Auto.Suggestions.Enabled
	}
	var b strings.Builder
	fmt.Fprintf(&b, "suggestions %s items=%d\n", f8OnOff(enabled), len(st.Items))
	for _, it := range st.Items {
		fmt.Fprintf(&b, "%s [%s] %s\n", it.ID, it.Status, f8Redact(it.Text))
	}
	if catalog {
		for _, d := range auto.Definitions() {
			fmt.Fprintf(&b, "%s %s: %s\n", d.ID, d.Feature, d.Trigger)
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func f8OnOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

// f8Blueprint validates a template name and its closed slot set. No provider
// call is made: the answer is the template's own steps.
func f8Blueprint(args []string) string {
	if len(args) == 0 || args[0] == "catalog" {
		var b strings.Builder
		for _, bp := range auto.BlueprintCatalog() {
			fmt.Fprintf(&b, "%s slots=%s: %s\n", bp.Name, strings.Join(bp.Slots, ","), bp.Description)
		}
		return strings.TrimSuffix(b.String(), "\n")
	}
	name := args[0]
	slots := map[string]string{}
	for _, arg := range args[1:] {
		k, v, ok := strings.Cut(arg, "=")
		if !ok || k == "" || v == "" {
			return "blueprint: slot " + cleanArg(arg) + " must be name=value"
		}
		slots[k] = v
	}
	return f8BlueprintReady(name, slots)
}

// f8BlueprintReady validates the closed slot set and renders one template.
func f8BlueprintReady(name string, slots map[string]string) string {
	for _, bp := range auto.BlueprintCatalog() {
		if bp.Name != name {
			continue
		}
		for k := range slots {
			if !slices.Contains(bp.Slots, k) {
				return "blueprint: unknown slot " + cleanArg(k)
			}
		}
		for _, s := range bp.Slots {
			if slots[s] == "" {
				return "blueprint: missing slot " + cleanArg(s)
			}
		}
		keys := make([]string, 0, len(slots))
		for k := range slots {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		return "blueprint " + name + " ready: " + strings.Join(bp.Steps, "; ") + " slots=" + strings.Join(keys, ",")
	}
	return "blueprint: unknown template " + cleanArg(name)
}

// f8Goal is one standing goal plus its subgoals (SLASH-030/037).
type f8Goal struct {
	Text     string   `json:"text"`
	MaxTurns int      `json:"max_turns"`
	Turns    int      `json:"turns"`
	Subgoals []string `json:"subgoals"`
}

func (a *f8SlashAdapter) goal(dir string, owner bool, sessionID string, args []string) string {
	if len(args) > 0 {
		switch args[0] {
		case "status", "show":
			return f8GoalStatus(dir, sessionID)
		case "clear":
			if msg := f8OwnerOnly(owner, "goal"); msg != "" {
				return msg
			}
			return f8GoalClear(dir, sessionID)
		}
	}
	if len(args) == 0 {
		return f8GoalStatus(dir, sessionID)
	}
	if msg := f8OwnerOnly(owner, "goal"); msg != "" {
		return msg
	}
	text := strings.TrimSpace(strings.Join(args, " "))
	g, err := auto.NewGoal(text, 0)
	if err != nil {
		return "goal: " + cleanLine(err.Error())
	}
	st, err := f8LoadSlash[f8Goal](f8StorePath(dir, "slash-goals.json"))
	if err != nil {
		return "goal: " + cleanLine(err.Error())
	}
	st.Sessions[sessionID] = f8Goal{Text: g.Text, MaxTurns: g.MaxTurns}
	if err := f8SaveSlash(f8StorePath(dir, "slash-goals.json"), &st); err != nil {
		return "goal: " + cleanLine(err.Error())
	}
	return "goal set (max " + strconv.Itoa(g.MaxTurns) + " turns): " + f8Redact(g.Text)
}

func f8GoalStatus(dir, sessionID string) string {
	st, err := f8LoadSlash[f8Goal](f8StorePath(dir, "slash-goals.json"))
	if err != nil {
		return "goal: " + cleanLine(err.Error())
	}
	g, ok := st.Sessions[sessionID]
	if !ok || g.Text == "" {
		return "goal: none"
	}
	return fmt.Sprintf("goal turns=%d/%d subgoals=%d: %s", g.Turns, g.MaxTurns, len(g.Subgoals), f8Redact(g.Text))
}

func f8GoalClear(dir, sessionID string) string {
	st, err := f8LoadSlash[f8Goal](f8StorePath(dir, "slash-goals.json"))
	if err != nil {
		return "goal: " + cleanLine(err.Error())
	}
	if _, ok := st.Sessions[sessionID]; !ok {
		return "goal: none"
	}
	delete(st.Sessions, sessionID)
	if err := f8SaveSlash(f8StorePath(dir, "slash-goals.json"), &st); err != nil {
		return "goal: " + cleanLine(err.Error())
	}
	return "goal cleared"
}

func parseLoopArgs(args []string) (f8Loop, string) {
	var out f8Loop
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--times":
			if i+1 >= len(args) {
				return out, "loop: --times needs a value"
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 1 || n > 1000 {
				return out, "loop: --times must be 1..1000"
			}
			out.Times = n
			i++
		case "--until":
			if i+1 >= len(args) {
				return out, "loop: --until needs RFC3339"
			}
			if _, err := time.Parse(time.RFC3339, args[i+1]); err != nil {
				return out, "loop: --until must be RFC3339"
			}
			out.Until = args[i+1]
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	if len(rest) < 2 {
		return out, "usage: /loop <seconds> <prompt> [--times N] [--until RFC3339]"
	}
	secs, err := strconv.Atoi(rest[0])
	if err != nil {
		return out, "loop: seconds must be an integer"
	}
	out.IntervalS = secs
	out.Prompt = strings.Join(rest[1:], " ")
	return out, ""
}

func (a *f8SlashAdapter) loop(dir string, owner bool, sessionID string, args []string) string {
	path := f8StorePath(dir, "slash-loops.json")
	st, err := f8LoadSlash[f8Loop](path)
	if err != nil {
		return "loop: " + cleanLine(err.Error())
	}
	if len(args) == 0 || args[0] == "status" {
		l, ok := st.Sessions[sessionID]
		if !ok {
			return "loop off"
		}
		return fmt.Sprintf("loop interval=%ds times=%d prompt=%s", l.IntervalS, l.Times, f8Redact(l.Prompt))
	}
	if msg := f8OwnerOnly(owner, "loop"); msg != "" {
		return msg
	}
	if args[0] == "clear" {
		if _, ok := st.Sessions[sessionID]; !ok {
			return "loop off"
		}
		delete(st.Sessions, sessionID)
		if err := f8SaveSlash(path, &st); err != nil {
			return "loop: " + cleanLine(err.Error())
		}
		return "loop cleared"
	}
	spec, msg := parseLoopArgs(args)
	if msg != "" {
		return msg
	}
	until := time.Time{}
	if spec.Until != "" {
		var perr error
		until, perr = time.Parse(time.RFC3339, spec.Until)
		if perr != nil {
			return "loop: --until must be RFC3339"
		}
	}
	if _, err := auto.NewLoop(auto.LoopSpec{Interval: time.Duration(spec.IntervalS) * time.Second, Prompt: spec.Prompt, Times: spec.Times, Until: until}); err != nil {
		return "loop: " + cleanLine(err.Error())
	}
	st.Sessions[sessionID] = spec
	if err := f8SaveSlash(path, &st); err != nil {
		return "loop: " + cleanLine(err.Error())
	}
	return fmt.Sprintf("loop saved interval=%ds times=%d", spec.IntervalS, spec.Times)
}

// f8Personas is the closed persona catalog.
func f8Personas() []string { return []string{"concise", "default", "mentor", "skeptic"} }

func (a *f8SlashAdapter) personality(dir string, owner bool, args []string) string {
	path := f8StorePath(dir, "personality.json")
	state := struct {
		Current string `json:"current"`
	}{}
	if err := f8Load(path, &state); err != nil {
		return "personality: " + cleanLine(err.Error())
	}
	current := state.Current
	if current == "" {
		current = "default"
	}
	if len(args) == 0 || args[0] == "status" || args[0] == "list" {
		return "personality " + current + "; presets: " + strings.Join(f8Personas(), " ")
	}
	if msg := f8OwnerOnly(owner, "personality"); msg != "" {
		return msg
	}
	if len(args) != 1 || !slices.Contains(f8Personas(), args[0]) {
		return "personality: unknown preset " + cleanArg(args[0])
	}
	if args[0] == current {
		return "personality " + current
	}
	state.Current = args[0]
	if err := f8Save(path, &state); err != nil {
		return "personality: " + cleanLine(err.Error())
	}
	return "personality " + state.Current
}

// f8SlashInsights renders the compact ledger report (SLASH-094).
