package main

import (
	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/provider"
)

// billedUsage is the conservative per-attempt projection of a priced route
// for the ledger rows of failed attempts (SEC6-11): the estimated input plus
// max_tokens at the route prices. Nil for an unpriced route (NULL/unknown).
// billedUsage is the conservative projection of one failed attempt: the raw
// input estimate inflated by the ADR-101 calibration maximum (1.5×) plus
// per-message overhead, and max_tokens output. It is an upper bound, never
// below the contract price.
func billedUsage(resp *provider.Response, messages int, maxTok int64) *event.Usage {
	if resp.Price == nil {
		return nil
	}
	input := resp.EstInputTokens + resp.EstInputTokens/2
	input += int64(messages) * 8 // per-message framing overhead
	if maxTok <= 0 {
		maxTok = provider.DefaultMaxTokens
	}
	return &event.Usage{Input: input, Output: maxTok, Price: eventPrice(resp.Price)}
}

// eventPrice converts the provider response price entry to the loop's wire
// type (loop/event is a leaf: it does not import budget).
func eventPrice(p *budget.Price) *event.Price {
	if p == nil {
		return nil
	}
	return &event.Price{
		InputPerMTok: p.InputPerMTok, CachedInputPerMTok: p.CachedInputPerMTok,
		OutputPerMTok: p.OutputPerMTok, Version: p.Version,
	}
}
