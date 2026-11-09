//go:build !minimal

package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/auto"
)

func (a *f8SlashAdapter) subgoal(dir string, owner bool, sessionID string, args []string) string {
	st, err := f8LoadSlash[f8Goal](f8StorePath(dir, "slash-goals.json"))
	if err != nil {
		return "subgoal: " + cleanLine(err.Error())
	}
	g, ok := st.Sessions[sessionID]
	if !ok || g.Text == "" {
		return "subgoal: no active goal; set one with /goal <text>"
	}
	if len(args) == 0 || args[0] == "list" {
		if len(g.Subgoals) == 0 {
			return "subgoal: none"
		}
		var b strings.Builder
		for i, s := range g.Subgoals {
			fmt.Fprintf(&b, "%d. %s\n", i+1, f8Redact(s))
		}
		return strings.TrimSuffix(b.String(), "\n")
	}
	if msg := f8OwnerOnly(owner, "subgoal"); msg != "" {
		return msg
	}
	switch args[0] {
	case "clear":
		g.Subgoals = nil
	case "remove":
		next, msg := f8SubgoalRemove(g.Subgoals, args)
		if msg != "" {
			return msg
		}
		g.Subgoals = next
	default:
		text := strings.TrimSpace(strings.Join(args, " "))
		if text == "" || len(g.Subgoals) >= 20 {
			return "subgoal: text is empty or the cap of 20 is reached"
		}
		g.Subgoals = append(g.Subgoals, text)
	}
	st.Sessions[sessionID] = g
	if err := f8SaveSlash(f8StorePath(dir, "slash-goals.json"), &st); err != nil {
		return "subgoal: " + cleanLine(err.Error())
	}
	return "subgoals " + strconv.Itoa(len(g.Subgoals))
}

// f8SubgoalRemove deletes one criterion by 1-based index or exact text.
func f8SubgoalRemove(list []string, args []string) ([]string, string) {
	if len(args) != 2 {
		return list, "usage: /subgoal remove <index|text>"
	}
	i, err := strconv.Atoi(args[1])
	if err == nil && i >= 1 && i <= len(list) {
		return slices.Delete(list, i-1, i), ""
	}
	if j := slices.Index(list, strings.Join(args[1:], " ")); j >= 0 {
		return slices.Delete(list, j, j+1), ""
	}
	return list, "subgoal: not found"
}

// f8Heartbeat is one stored heartbeat configuration (SLASH-031).
type f8Heartbeat struct {
	IntervalS int    `json:"interval_s"`
	Prompt    string `json:"prompt"`
	Paused    bool   `json:"paused"`
}

func (a *f8SlashAdapter) heartbeat(dir string, owner bool, sessionID string, args []string) string {
	path := f8StorePath(dir, "slash-heartbeat.json")
	st, err := f8LoadSlash[f8Heartbeat](path)
	if err != nil {
		return "heartbeat: " + cleanLine(err.Error())
	}
	h, ok := st.Sessions[sessionID]
	if len(args) == 0 || args[0] == "status" {
		if !ok {
			return "heartbeat off"
		}
		return fmt.Sprintf("heartbeat %s interval=%ds paused=%t", f8OnOff(true), h.IntervalS, h.Paused)
	}
	if msg := f8OwnerOnly(owner, "heartbeat"); msg != "" {
		return msg
	}
	switch args[0] {
	case "pause", "resume":
		if !ok {
			return "heartbeat off"
		}
		h.Paused = args[0] == "pause"
	case "clear":
		if !ok {
			return "heartbeat off"
		}
		delete(st.Sessions, sessionID)
		if err := f8SaveSlash(path, &st); err != nil {
			return "heartbeat: " + cleanLine(err.Error())
		}
		return "heartbeat cleared"
	case "set":
		next, msg := f8HeartbeatSet(args)
		if msg != "" {
			return msg
		}
		h = next
	default:
		return "usage: /heartbeat [status|pause|resume|clear|set <seconds> <prompt>]"
	}
	st.Sessions[sessionID] = h
	if err := f8SaveSlash(path, &st); err != nil {
		return "heartbeat: " + cleanLine(err.Error())
	}
	return "heartbeat saved interval=" + strconv.Itoa(h.IntervalS) + "s paused=" + strconv.FormatBool(h.Paused)
}

// f8HeartbeatSet validates one `set` invocation through auto.NewHeartbeat.
func f8HeartbeatSet(args []string) (f8Heartbeat, string) {
	if len(args) < 3 {
		return f8Heartbeat{}, "usage: /heartbeat set <seconds> <prompt>"
	}
	secs, err := strconv.Atoi(args[1])
	if err != nil {
		return f8Heartbeat{}, "heartbeat: seconds must be an integer"
	}
	prompt := strings.Join(args[2:], " ")
	state, err := auto.NewHeartbeat(time.Duration(secs)*time.Second, prompt)
	if err != nil {
		return f8Heartbeat{}, "heartbeat: " + cleanLine(err.Error())
	}
	return f8Heartbeat{IntervalS: int(state.Interval().Seconds()), Prompt: prompt}, ""
}

// f8Loop is one stored recurring prompt (SLASH-034).
type f8Loop struct {
	IntervalS int    `json:"interval_s"`
	Prompt    string `json:"prompt"`
	Times     int    `json:"times"`
	Until     string `json:"until,omitempty"`
}

func f8SlashInsights(ctx context.Context, dir string, args []string) string {
	days := 7
	if len(args) > 0 {
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 0 || len(args) > 1 {
			return "usage: /insights [days]"
		}
		days = n
	}
	cutoff, err := insightCutoff(days, "")
	if err != nil {
		return "insights: " + cleanLine(err.Error())
	}
	rows, err := usageRows(ctx, dir, "day")
	if err != nil {
		return "insights: " + cleanLine(err.Error())
	}
	in := buildInsights(rows, cutoff)
	var b strings.Builder
	label := "all"
	if cutoff != "" {
		label = "since " + cutoff
	}
	fmt.Fprintf(&b, "insights %s calls=%d input=%d output=%d cost=%.4f\n", label, in.Total.Calls, in.Total.Input, in.Total.Output, float64(in.Total.CostMicros)/1e6)
	start := max(0, len(in.Rows)-10)
	for _, r := range in.Rows[start:] {
		fmt.Fprintf(&b, "%s calls=%d in=%d out=%d\n", r.Day, r.Calls, r.Input, r.Output)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func (a *f8SlashAdapter) finishPets(dir string, st *f8PetsState, changed bool, err error, b *strings.Builder) string {
	if err != nil {
		return "pet: " + cleanLine(err.Error())
	}
	if changed {
		if serr := f8Save(f8StorePath(dir, "pets.json"), st); serr != nil {
			return "pet: " + cleanLine(serr.Error())
		}
	}
	if b == nil {
		return "pet " + st.Active
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// hatchSlash implements /hatch, /generate-pet (SLASH-073) with a local,
// deterministic pet. Network generation is opt-in and unavailable here.
func (a *f8SlashAdapter) hatchSlash(dir string, owner bool, args []string) string {
	if msg := f8OwnerOnly(owner, "hatch"); msg != "" {
		return msg
	}
	desc := strings.TrimSpace(strings.Join(args, " "))
	if desc == "" {
		return "usage: /hatch <description>"
	}
	if strings.Contains(desc, "--remote") {
		return "module_unavailable: petdex: network generation is not available in this build"
	}
	sum := sha256.Sum256([]byte(desc))
	name := "pet-" + hex.EncodeToString(sum[:4])
	st := f8PetsState{}
	if err := f8Load(f8StorePath(dir, "pets.json"), &st); err != nil {
		return "hatch: " + cleanLine(err.Error())
	}
	if petsFind(&st, name) == nil {
		st.Items = append(st.Items, f8Pet{
			Name: name, Style: "hatched", Description: desc, Scale: "medium",
			SHA256: hex.EncodeToString(sum[:]), InstalledAt: time.Now().Unix(),
		})
		slices.SortFunc(st.Items, func(x, y f8Pet) int { return strings.Compare(x.Name, y.Name) })
	}
	if st.Active == "" {
		st.Active = name
	}
	if err := f8Save(f8StorePath(dir, "pets.json"), &st); err != nil {
		return "hatch: " + cleanLine(err.Error())
	}
	return "hatched " + name + " scale=medium"
}

// curatorSlash implements /curator status|run|pause|resume|pin|unpin|restore|
// list-archived (SLASH-079).

// f8DebugLink is the one-use local artifact behind /debug (SLASH-102).
type f8DebugLink struct {
	Token   string `json:"token"`
	Path    string `json:"path"`
	Expires int64  `json:"expires"`
}

func (a *f8SlashAdapter) debugSlash(dir string, owner bool, args []string) string {
	if msg := f8OwnerOnly(owner, "debug"); msg != "" {
		return msg
	}
	if len(args) == 0 {
		return "usage: /debug share|delete <token>"
	}
	switch args[0] {
	case "share":
		return f8DebugShare(dir)
	case "delete":
		if len(args) != 2 {
			return "usage: /debug delete <token>"
		}
		return f8DebugDelete(dir, args[1])
	default:
		return "usage: /debug share|delete <token>"
	}
}

func f8DebugShare(dir string) string {
	cfg, paths, data, err := f8LoadSetup("", dir)
	if err != nil {
		return "debug: " + cleanLine(err.Error())
	}
	tok := make([]byte, 16)
	if _, err := rand.Read(tok); err != nil {
		return "debug: token: " + cleanLine(err.Error())
	}
	token := hex.EncodeToString(tok)
	path := filepath.Join(data, "debug", token+".txt")
	report := f8DumpReport(&cfg, paths, data)
	payload := f8Redact(f8DumpText(&report))
	if err := f8WriteFile(path, []byte(payload)); err != nil {
		return "debug: " + cleanLine(err.Error())
	}
	link := f8DebugLink{Token: token, Path: path, Expires: time.Now().Add(10 * time.Minute).Unix()}
	if err := f8Save(f8StorePath(dir, "debug-link.json"), &link); err != nil {
		return "debug: " + cleanLine(err.Error())
	}
	return fmt.Sprintf("debug shared %s token=%s (one use, 10m)", path, token)
}

func f8DebugDelete(dir, token string) string {
	link := f8DebugLink{}
	if err := f8Load(f8StorePath(dir, "debug-link.json"), &link); err != nil {
		return "debug: " + cleanLine(err.Error())
	}
	if link.Token == "" || link.Token != token {
		return "debug: token already used or unknown"
	}
	if link.Expires != 0 && time.Now().Unix() > link.Expires {
		return "debug: token expired"
	}
	if err := os.Remove(link.Path); err != nil && !os.IsNotExist(err) { // #nosec G304 -- path recorded by this command
		return "debug: " + cleanLine(err.Error())
	}
	if err := f8Save(f8StorePath(dir, "debug-link.json"), &f8DebugLink{}); err != nil {
		return "debug: " + cleanLine(err.Error())
	}
	return "debug deleted " + token
}
