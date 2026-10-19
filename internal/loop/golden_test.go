package loop

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/damnbot/damnbot/internal/loop/event"
)

var update = flag.Bool("update", false, "update golden files") //nolint:gochecknoglobals // -update is the standard golden-test flag (E-68).

// TestGoldenTurnOrdering (E-68): the stream update order of a two-iteration
// turn (text, tool fragments, result, final answer) is fixed.
func TestGoldenTurnOrdering(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{
		func(_ context.Context, _ *Request, emit func(Delta)) (Reply, error) {
			emit(Delta{Text: "Let me look. "})
			emit(Delta{ToolCalls: []ToolCallDelta{
				{Index: 0, HasIndex: true, ID: "c1", Name: "read_file", Arguments: `{"pa`},
				{Index: 0, HasIndex: true, Arguments: `th":"a.go"}`},
			}})
			return Reply{FinishReason: FinishToolCalls, Usage: event.Usage{Input: 10, Output: 4}, Model: "fake/m"}, nil
		},
		textScript("Done.", event.Usage{Input: 12, Output: 2}),
	}
	f.tools.add("read_file", &toolStub{dispatch: func(context.Context, *Prepared) (ToolResult, error) {
		return ToolResult{Content: "file-content"}, nil
	}})
	ups := f.run(t)
	var got []goldenUpdate
	for _, u := range ups {
		got = append(got, goldenUpdate{
			Kind: string(u.Kind), Text: u.Text, Tool: u.ToolName,
			Status: u.Status, Stop: string(u.Stop), Detail: string(u.Detail),
		})
	}
	b, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	const path = "testdata/turn_ordering.golden.json"
	if *update {
		if mkErr := os.MkdirAll("testdata", 0o750); mkErr != nil {
			t.Fatal(mkErr)
		}
		if wErr := os.WriteFile(path, b, 0o600); wErr != nil {
			t.Fatal(wErr)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -run Golden -update)", err)
	}
	if !bytes.Equal(b, want) {
		t.Errorf("turn ordering changed:\n--- got ---\n%s\n--- want ---\n%s\n(re-run with -update after review)", b, want)
	}
}

type goldenUpdate struct {
	Kind   string `json:"kind"`
	Text   string `json:"text,omitempty"`
	Tool   string `json:"tool,omitempty"`
	Status string `json:"status,omitempty"`
	Stop   string `json:"stop,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// TestGoldenToolRendering (E-68): every model-facing tool shape is golden.
func TestGoldenToolRendering(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		cfg       Config
		res       ToolResult
		noSpiller bool
	}{
		{
			name: "success_unchanged",
			res:  ToolResult{Content: "{\"x\":1}\nWarning: appended by the tool"},
		},
		{
			name: "error_envelope",
			res: ToolResult{Err: &ToolError{
				Code: CodeInvalidArgs, Message: "/path: required property missing",
				Hint: "read_file requires {\"path\": string}",
			}},
		},
		{
			name: "spilled",
			cfg:  Config{MaxInline: 10},
			res:  ToolResult{Content: "0123456789abcdefghij"},
		},
		{
			name:      "too_large_without_spiller",
			cfg:       Config{MaxInline: 4},
			res:       ToolResult{Content: "0123456789"},
			noSpiller: true,
		},
		{
			name:      "turn_aggregate_truncated",
			cfg:       Config{MaxInline: 100, MaxInlineTurn: 3},
			res:       ToolResult{Content: "0123456789"},
			noSpiller: true,
		},
	}
	spiller := &fakeSpiller{ref: "blob://sha256/abc"}
	out := map[string]string{}
	for _, c := range cases {
		deps := Deps{Spiller: spiller}
		if c.noSpiller {
			deps.Spiller = nil
		}
		cfg := c.cfg
		withDefaults(&cfg)
		turn := &turn{cfg: cfg, deps: deps}
		out[c.name] = turn.modelContent(context.Background(), &c.res)
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	b = append(b, '\n')
	const path = "testdata/tool_render.golden.json"
	if *update {
		if mkErr := os.MkdirAll("testdata", 0o750); mkErr != nil {
			t.Fatal(mkErr)
		}
		if wErr := os.WriteFile(path, b, 0o600); wErr != nil {
			t.Fatal(wErr)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -run Golden -update)", err)
	}
	if !bytes.Equal(b, want) {
		t.Errorf("model-facing tool rendering changed:\n--- got ---\n%s\n--- want ---\n%s\n(re-run with -update after review)", b, want)
	}
}
