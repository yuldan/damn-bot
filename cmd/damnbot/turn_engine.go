// Turn machinery shared by the headless runner and the TUI.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/modules"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/tools"
)

type turnEngine struct {
	loop *loop.Loop
	disp *tools.Dispatcher
}

// newTurnEngine builds one turn; it is I/O-free until the caller prompts.
func newTurnEngine(ctx context.Context, cfg *config.Config, o *runOptions, model *runModel, modelRef string, red *redact.Redactor, spill tools.Spill) (*turnEngine, error) {
	asm, turn, err := newRunAssembler(cfg, red)
	if err != nil {
		return nil, err
	}
	win := runWindow(ctx, model, modelRef)
	degraded := win > 0 && win < assemble.DegradedWindow
	ctxIn := contextTierInput(ctx, model, o, degraded)
	ctxTier, err := asm.Context(ctx, ctxIn)
	if err != nil {
		return nil, fmt.Errorf("assemble context: %w", err)
	}
	toolList, err := runTools(model.registry, model.toolsets.Allowed)
	if err != nil {
		return nil, err
	}
	comp, _, err := newRunCompactor(ctx, model, modelRef, asm, toolList, ctxTier.Text, red)
	if err != nil {
		return nil, err
	}
	disp, err := newRunDispatcher(o, model, red, spill)
	if err != nil {
		return nil, err
	}
	compactAt, maxInline := 75, 0
	if degraded {
		compactAt, maxInline = 60, 2000 // PROV-02 degraded profile (SPEC §22.1)
	}
	lp, err := loop.New(&loop.Config{
		MaxIterations:   cfg.Loop.MaxIterations,
		MaxInputTokens:  int64(cfg.Loop.TurnMaxInputTok),
		MaxOutputTokens: int64(cfg.Loop.TurnMaxOutputTok),
		MaxWall:         time.Duration(cfg.Loop.MaxWallS) * time.Second,
		MaxIdentical:    cfg.Loop.MaxIdentical,
		ParallelTools:   cfg.Loop.ParallelTools,
		CompactAt:       compactAt,
		MaxInline:       maxInline,
		Now:             o.now,
	}, &loop.Deps{
		Provider:       model.provider,
		Assembler:      &assemblerAdapter{asm: asm, turn: turn, tools: toolList, contextTier: ctxTier.Text, model: modelRef, maxTokens: cfg.Provider.MaxOutput, compactor: comp, window: win, store: model.store, markTainted: disp.MarkTainted, db: model.db, catalog: catalogGroups(modules.Default())},
		Compactor:      comp,
		Toolset:        &toolsetAdapter{disp: disp, allowed: model.toolsets.Allowed},
		Store:          model.store,
		Ledger:         model.ledger,
		Carriers:       &ctxCarrierStore{db: model.db},
		Redactor:       redactorAdapter{red: red},
		Origin:         o.approvalOrigin,
		Approver:       o.approver,
		ValidateAnswer: o.validate,

		NewRedactStream: func() loop.StreamFilter { return red.NewStream() },
	})
	if err != nil {
		return nil, fmt.Errorf("loop: %w", err)
	}
	return &turnEngine{loop: lp, disp: disp}, nil
}

// newRunDispatcher builds the single tool pipeline shared by turns and !cmd.
func newRunDispatcher(o *runOptions, model *runModel, red *redact.Redactor, spill tools.Spill) (*tools.Dispatcher, error) {
	disp, err := tools.NewDispatcher(&tools.Config{
		Registry:         model.registry,
		Journal:          loopTranscript{db: model.db, sid: model.sessionID},
		Spill:            spill,
		Redactor:         red,
		Mode:             approvalMode(o),
		Deferred:         model.modules.deferred,
		WorkspaceTrusted: model.trusted,
		Cwd:              model.cwd,
		WindowTokens:     model.inlineCap,
		CharsPerToken:    charsPerToken(o.prompt),
		Origin:           o.approvalOrigin,
		Approver:         o.toolApprover,
	})
	if err != nil {
		return nil, fmt.Errorf("tools dispatcher: %w", err)
	}
	return disp, nil
}
