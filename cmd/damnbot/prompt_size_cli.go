//go:build !minimal

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/modules"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/toolset"
)

// promptToolSize is one tool schema's contribution to the request bytes.
type promptToolSize struct {
	Name  string `json:"name"`
	Bytes int    `json:"bytes"`
}

// promptSizeReport is the one byte calculation behind `damnbot prompt-size`
// (CLI-072/OPS-017). It measures the same tiers the run assembler sends:
// stable prompt, deferred catalog, and the declared tool schemas.
type promptSizeReport struct {
	Model        string           `json:"model"`
	SystemBytes  int              `json:"system_bytes"`
	StableBytes  int              `json:"stable_bytes"`
	CatalogBytes int              `json:"catalog_bytes"`
	ToolsBytes   int              `json:"tools_bytes"`
	TotalBytes   int              `json:"total_bytes"`
	Tools        []promptToolSize `json:"tools"`
}

func promptSizeUsage(w io.Writer) {
	say(w, "usage: damnbot prompt-size [--model provider/model] [--config FILE] [--json]\n")
}

// runPromptSize implements `damnbot prompt-size` (CLI-072, OPS-017). The
// registry is built in a throwaway data dir, so the command never touches the
// operator's sessions, keys, or provider credentials.
func runPromptSize(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("prompt-size", flag.ContinueOnError)
	fs.SetOutput(stderr)
	model := fs.String("model", "", "provider/model whose prompt is measured")
	configPath := fs.String("config", "", "config file (default: the active profile)")
	asJSON := fs.Bool("json", false, "machine-readable breakdown")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		say(stderr, "prompt-size: unexpected argument %q\n", cleanArg(fs.Arg(0)))
		return 2
	}
	o := runOptions{configPath: *configPath, model: *model}
	cfg, _, configDir, home, err := loadRunEnv(&o)
	if err != nil {
		say(stderr, "prompt-size: %s\n", cleanLine(err.Error()))
		return 1
	}
	modelRef := *model
	if modelRef == "" {
		modelRef = cfg.Provider.Model
	}
	if modelRef == "" {
		say(stderr, "prompt-size: --model or provider.model is required\n")
		return 1
	}
	rep, err := measurePromptSize(context.Background(), &cfg, &o, modelRef, configDir, home)
	if err != nil {
		say(stderr, "prompt-size: %s\n", cleanLine(err.Error()))
		return 1
	}
	if *asJSON {
		b, merr := json.MarshalIndent(rep, "", "  ")
		if merr != nil {
			return 1
		}
		return say(stdout, "%s\n", b)
	}
	say(stdout, "prompt-size: model %s\n", rep.Model)
	say(stdout, "system prompt: %d B (stable %d B + catalog %d B)\n", rep.SystemBytes, rep.StableBytes, rep.CatalogBytes)
	say(stdout, "tool schemas: %d tools, %d B\n", len(rep.Tools), rep.ToolsBytes)
	for _, t := range rep.Tools {
		say(stdout, "  %-24s %6d B\n", t.Name, t.Bytes)
	}
	say(stdout, "total: %d B (system + tools JSON)\n", rep.TotalBytes)
	return 0
}

// measurePromptSize builds the real run wiring in a temporary data dir and
// assembles one request without a turn. The assembler is the same one the
// loop uses, so the byte count tracks the production payload.
func measurePromptSize(ctx context.Context, cfg *config.Config, o *runOptions, modelRef, configDir, home string) (promptSizeReport, error) {
	tmp, err := os.MkdirTemp("", "damnbot-prompt-size-")
	if err != nil {
		return promptSizeReport{}, fmt.Errorf("temp data dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }() //nolint:errcheck // temporary measurement dir
	red := redact.New(redact.Options{})
	w, err := newRunWiring(ctx, cfg, tmp, modelRef, o, red, configDir, home, nil)
	if err != nil {
		return promptSizeReport{}, fmt.Errorf("wiring: %w", err)
	}
	defer func() { _ = w.fail(nil) }() //nolint:errcheck // measurement closes the throwaway wiring
	sel, err := toolset.Resolve(cfg.Toolsets, runtime.GOOS, availableNames(w.registry, w.modules))
	if err != nil {
		return promptSizeReport{}, fmt.Errorf("toolsets: %w", err)
	}
	declared, err := runTools(w.registry, sel.Allowed)
	if err != nil {
		return promptSizeReport{}, fmt.Errorf("tools: %w", err)
	}
	asm, _, err := newRunAssembler(cfg, red, time.Local)
	if err != nil {
		return promptSizeReport{}, err
	}
	groups := catalogGroups(modules.Default())
	catalog := asm.Catalog(groups)
	opts := stableReply(cfg)
	opts.Deferred = catalog != ""
	req, _, err := asm.Build(ctx, &assemble.SessionView{
		Phase:   assemble.PhaseF1,
		Stable:  opts,
		Tools:   declared,
		Catalog: groups,
	})
	if err != nil {
		return promptSizeReport{}, fmt.Errorf("assemble: %w", err)
	}
	system := ""
	if len(req.Messages) > 0 && req.Messages[0].Role == "system" {
		system = req.Messages[0].Content
	}
	toolsJSON, err := assemble.MarshalTools(req.Tools)
	if err != nil {
		return promptSizeReport{}, fmt.Errorf("tools json: %w", err)
	}
	sizes := make([]promptToolSize, 0, len(req.Tools))
	for _, t := range req.Tools {
		raw, merr := assemble.MarshalTools([]assemble.Tool{t})
		if merr != nil {
			return promptSizeReport{}, fmt.Errorf("tool %s json: %w", t.Name, merr)
		}
		sizes = append(sizes, promptToolSize{Name: t.Name, Bytes: len(raw)})
	}
	slices.SortFunc(sizes, func(a, b promptToolSize) int {
		if a.Bytes != b.Bytes {
			return b.Bytes - a.Bytes
		}
		return strings.Compare(a.Name, b.Name)
	})
	return promptSizeReport{
		Model:        modelRef,
		SystemBytes:  len(system),
		StableBytes:  len(assemble.Stable(opts)),
		CatalogBytes: len(catalog),
		ToolsBytes:   len(toolsJSON),
		TotalBytes:   len(system) + len(toolsJSON),
		Tools:        sizes,
	}, nil
}
