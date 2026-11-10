package memoryproviders

import (
	"context"
	"fmt"
	"strings"
)

// rewriteSystemPrompt is the English auxiliary prompt of MEM-009. It is stable
// so a prompt-cache breakpoint is meaningful across recalls.
const rewriteSystemPrompt = "Rewrite the search query for a long-term memory store. " +
	"Keep entities, names and dates. Return one line of plain text, at most 200 characters, without explanation."

// maxRewriteRunes bounds the rewritten query returned by the auxiliary model.
const maxRewriteRunes = 400

// Completer is the auxiliary LLM seam of MEM-009. The composition root passes
// the run provider with purpose=turn; tests pass a fake.
type Completer interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

type llmRewriter struct {
	completer Completer
}

// NewLLMRewriter builds the auxiliary query rewriter. A nil completer returns
// nil so the caller can leave MEM-009 off.
func NewLLMRewriter(c Completer) Rewriter {
	if c == nil {
		return nil
	}
	return &llmRewriter{completer: c}
}

// Rewrite returns a single trimmed line or the original query when the model
// answers empty. The returned text never exceeds maxRewriteRunes.
func (r *llmRewriter) Rewrite(ctx context.Context, query string) (string, error) {
	if strings.TrimSpace(query) == "" {
		return "", nil
	}
	out, err := r.completer.Complete(ctx, rewriteSystemPrompt, query)
	if err != nil {
		return "", fmt.Errorf("query rewrite: %w", err)
	}
	out = strings.TrimSpace(strings.Split(out, "\n")[0])
	if out == "" {
		return query, nil
	}
	runes := []rune(out)
	if len(runes) > maxRewriteRunes {
		out = strings.TrimSpace(string(runes[:maxRewriteRunes]))
	}
	return out, nil
}
