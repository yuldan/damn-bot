package assemble

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"strings"
)

// fillBody grows seed until heading+"\n"+body reaches cap tokens, then trims
// to the largest prefix that fits. " data" is deliberate: o200k_base spends
// one token per five bytes on it, so the ASCII estimator used here (4 bytes
// per token) over-estimates rather than under-estimates the bench measurement.
// It is fixture-only.
func fillBody(heading, seed string, cap int) string {
	c := asciiCounter()
	body := seed
	for c.Count(heading+"\n"+body) < cap {
		body += " data"
	}
	return truncateTokens(body, cap-c.Count(heading+"\n"), c)
}

// fixtureContext fills every context sub-cap of the f1-core P1 fixture
// (ADR-066 butir 1): facts 60, workspace 150, a 20 KB AGENTS.md clipped to
// 400, memory 300 with a pinned prefix, skills 150.
func fixtureContext() *ContextInput {
	return &ContextInput{
		Facts:     fillBody("# Environment", "os=linux/amd64 shell=bash cwd=/work/damnbot frontend=tui", CapFacts),
		Workspace: fillBody("# Workspace", "branch=main dirty=false go=1.26.9 verify=go test ./...", CapWorkspace),
		Files:     []ContextFile{{Path: "AGENTS.md"}},
		Memory: []Memory{
			{Text: fillBody("# Memory", "pinned: repository uses Go and TOML", CapPinned), Pinned: true},
			{Text: strings.Repeat("approved memory: run go test before answering. ", 40)},
			{Text: strings.Repeat("approved memory: keep edits small and verified. ", 40)},
		},
		Skills: []string{
			"code-review-" + strings.Repeat("x", 400),
			"diagnose-" + strings.Repeat("y", 400),
			"tdd-" + strings.Repeat("z", 400),
		},
		Reader: fixtureReader{"AGENTS.md": strings.Repeat("Use read_file, grep and glob before editing. Run go test. ", 800)},
	}
}

type fixtureReader map[string]string

func (r fixtureReader) Open(_ context.Context, path string) (io.ReadCloser, error) {
	body, ok := r[path]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return io.NopCloser(strings.NewReader(body)), nil
}

// fixtureTools is the F1 eager surface: 8 core tools, no tool_search or
// tool_invoke (SPEC §5.1, ADR-066). Shell carries the F5 background parameter;
// FilterTools(PhaseF1) is what removes it.
func fixtureTools() []Tool {
	return []Tool{
		{
			Name:        "read_file",
			Description: "Read a text file. Use offset and limit to page; outline=true lists symbols.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1},"offset":{"type":"integer"},"limit":{"type":"integer","minimum":1,"maximum":2000},"outline":{"type":"boolean"}},"required":["path"],"additionalProperties":false}`),
		},
		{
			Name:        "write_file",
			Description: "Write a file atomically. Needs a tag when the file exists.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1},"content":{"type":"string","maxLength":1000000},"tag":{"type":"string","pattern":"^[0-9a-f]{8}$"}},"required":["path","content"],"additionalProperties":false}`),
		},
		{
			Name:        "edit_file",
			Description: "Apply exact edits atomically. Needs tag from read_file or grep.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1},"tag":{"type":"string","pattern":"^[0-9a-f]{8}$"},"edits":{"type":"array","minItems":1,"maxItems":50,"items":{"type":"object","properties":{"old_string":{"type":"string","minLength":1},"new_string":{"type":"string"},"replace_all":{"type":"boolean"},"start_line":{"type":"integer"},"end_line":{"type":"integer"},"new_text":{"type":"string"}},"additionalProperties":false}}},"required":["path","tag","edits"],"additionalProperties":false}`),
		},
		{
			Name:        "glob",
			Description: "List files matching a doublestar pattern, gitignore-aware.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","minLength":1},"path":{"type":"string"},"sort":{"type":"string","enum":["path","mtime"]},"offset":{"type":"integer"},"limit":{"type":"integer","minimum":1,"maximum":2000}},"required":["pattern"],"additionalProperties":false}`),
		},
		{
			Name:        "grep",
			Description: "Search file contents with RE2, gitignore-aware and deterministic.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string","minLength":1},"path":{"type":"string"},"glob":{"type":"string"},"type":{"type":"string"},"case":{"type":"string","enum":["smart","sensitive","insensitive"]},"context":{"type":"integer","minimum":0,"maximum":5},"mode":{"type":"string","enum":["matches","files","count"]},"offset":{"type":"integer"},"limit":{"type":"integer","minimum":1,"maximum":500}},"required":["pattern"],"additionalProperties":false}`),
		},
		{
			Name:        "shell",
			Description: "Run a shell command with a timeout. Set background=true to run detached.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","minLength":1,"maxLength":65536},"timeout_s":{"type":"integer","minimum":1,"maximum":600},"cwd":{"type":"string"},"background":{"type":"boolean"},"session":{"type":"string","pattern":"^[a-z0-9_-]{1,32}$"},"stdin":{"type":"string","maxLength":65536}},"required":["command"],"additionalProperties":false}`),
		},
		{
			Name:        "web_fetch",
			Description: "Fetch an http(s) URL as text, bounded and untrusted.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","pattern":"^https?://"},"max_chars":{"type":"integer","minimum":500,"maximum":100000}},"required":["url"],"additionalProperties":false}`),
		},
		{
			Name:        "todo",
			Description: "Replace the plan list (at most 30 items) with status pending, in_progress or done.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"items":{"type":"array","maxItems":30,"items":{"type":"object","properties":{"id":{"type":"string"},"text":{"type":"string","maxLength":200},"status":{"type":"string","enum":["pending","in_progress","done"]}},"required":["id","text","status"],"additionalProperties":false}}},"required":["items"],"additionalProperties":false}`),
		},
	}
}
