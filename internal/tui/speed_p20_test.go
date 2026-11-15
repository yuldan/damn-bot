package tui

// TUI-only slash rows of the P28 probe: /queue list, /context and /theme
// (the /skin alias) live in the interactive surface, not in the gateway
// dispatcher. The model and its fakes come from the package test helpers, so
// the measured path is the real parser plus handler.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type tuiProbeResult struct {
	SamplesMS []float64 `json:"samples_ms"`
	Error     string    `json:"error,omitempty"`
	Source    string    `json:"source,omitempty"`
}

func TestSpeedP20TUIProbe(t *testing.T) {
	outDir := os.Getenv("DAMNBOT_SPEED_OUT")
	if outDir == "" {
		t.Skip("DAMNBOT_SPEED_OUT unset (run bench/speed/run-damnbot.sh)")
	}
	m, _, _ := newTestApp(t)
	out := map[string]tuiProbeResult{}
	for id, text := range map[string]string{
		"SLASH-028": "/queue list",
		"SLASH-040": "/context",
		"SLASH-061": "/theme",
		"MEM-027":   "/context",
	} {
		res := tuiProbeResult{Source: "internal/tui submit: " + text}
		for i := range 1 + 20 {
			started := time.Now()
			m.input.SetValue(text)
			if cmd := m.submit(); cmd != nil {
				m.Update(cmd())
			}
			if i > 0 {
				res.SamplesMS = append(res.SamplesMS, float64(time.Since(started).Nanoseconds())/1e6)
			}
		}
		out[id] = res
	}
	b, err := json.MarshalIndent(map[string]any{"results": out}, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	if err := os.MkdirAll(outDir, 0o750); err != nil { //nolint:gosec // outDir is DAMNBOT_SPEED_OUT from the harness
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "tui.json"), b, 0o600); err != nil { //nolint:gosec // harness output under DAMNBOT_SPEED_OUT
		t.Fatal(err)
	}
}
