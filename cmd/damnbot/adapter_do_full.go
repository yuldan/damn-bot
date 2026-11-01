//go:build !minimal

package main

import (
	"context"

	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/provider"
)

func (p *providerAdapter) do(ctx context.Context, purpose provider.Purpose, req *loop.Request, emit func(loop.Delta)) (loop.Reply, error) {
	_, model := p.routeModel(purpose, req.Model)
	preq := &provider.Request{Model: model, MaxTokens: req.MaxTokens, CacheBreakAfter: req.CacheBreakAfter, Origin: p.origin}
	for _, m := range req.Messages {
		pm := provider.Message{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, Carriers: providerCarriers(m.Carriers)}
		for _, c := range m.ToolCalls {
			pm.ToolCalls = append(pm.ToolCalls, provider.ToolCall{ID: c.ID, Name: c.Name, ArgumentsRaw: c.Arguments})
		}
		preq.Messages = append(preq.Messages, pm)
	}
	for _, t := range req.Tools {
		preq.Tools = append(preq.Tools, provider.ToolSchema{Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	var onChunk func(provider.Chunk)
	if emit != nil {
		onChunk = func(ch provider.Chunk) { emit(loop.Delta{Text: string(ch.Content)}) }
	}
	var resp provider.Response
	var err error
	if p.pooled != nil {
		resp, err = p.pooled.Do(ctx, purpose, preq, onChunk)
	} else {
		resp, err = p.client.Do(ctx, purpose, preq, onChunk)
	}
	warnings := p.notes.take()
	maxTok := int64(req.MaxTokens)
	if maxTok <= 0 {
		maxTok = provider.DefaultMaxTokens
	}
	billed := billedUsage(&resp, maxTok)
	if err != nil {
		se := streamError(err)
		se.Billed = resp.BilledFailed
		se.Usage = billed
		return loop.Reply{}, se
	}
	reply := loop.Reply{
		Content: resp.Content, FinishReason: resp.FinishReason, Usage: event.Usage{
			Input: resp.Usage.Input, CachedInput: resp.Usage.CachedInput,
			CacheCreation: resp.Usage.CacheCreation,
			Output:        resp.Usage.Output, CostUSD: resp.Usage.CostUSD,
			Price: eventPrice(resp.Price), TTFT: resp.TTFT,
		},
		Model: resp.Model, FallbackFrom: resp.FallbackFrom, Attempts: resp.Attempts,
		Carriers: eventCarriers(resp.Carriers), RejectedCarriers: eventCarriers(resp.RejectedCarriers),
		CarrierDrops: eventDrops(resp.CarrierDrops), ReasoningText: resp.ReasoningText,
		BudgetWarnings: warnings,
		BilledFailed:   resp.BilledFailed,
		BilledUsage:    billed,
	}
	for _, c := range resp.ToolCalls {
		reply.ToolCalls = append(reply.ToolCalls, loop.ToolCall{ID: c.ID, Name: c.Name, Arguments: c.ArgumentsRaw})
	}
	return reply, nil
}
