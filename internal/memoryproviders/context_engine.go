package memoryproviders

import (
	"context"
	"fmt"
)

// ContextEngineCompressor is the built-in engine. It is the only name the
// closed slot accepts (MEM-023/PLUG-014).
const ContextEngineCompressor = "compressor"

// ContextMessage is one read-only message handed to an engine.
type ContextMessage struct {
	Role    string
	Content string
}

// ContextInput is the immutable view an engine renders from.
type ContextInput struct {
	System   string
	Summary  string
	Messages []ContextMessage
}

// ContextOutput is the rendered view. An engine never writes the store.
type ContextOutput struct {
	Messages []ContextMessage
}

// ContextEngine is the opt-in plugin seam. The built-in compressor is a
// pass-through; an alternative engine is linked into the binary, never loaded
// from config or disk.
type ContextEngine interface {
	Name() string
	Render(ctx context.Context, in ContextInput) (ContextOutput, error)
}

type compressorEngine struct{}

func (compressorEngine) Name() string { return ContextEngineCompressor }

// Render returns a copy; the caller's slice is never mutated (read-only).
func (compressorEngine) Render(_ context.Context, in ContextInput) (ContextOutput, error) {
	return ContextOutput{Messages: append([]ContextMessage(nil), in.Messages...)}, nil
}

// LookupContextEngine resolves the configured engine. An empty name is the
// default-off state and returns [ErrUnavailable]; an unknown name is rejected.
func LookupContextEngine(name string) (ContextEngine, error) {
	switch name {
	case "":
		return nil, fmt.Errorf("%w: context engine is off", ErrUnavailable)
	case ContextEngineCompressor:
		return compressorEngine{}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownEngine, name)
	}
}
