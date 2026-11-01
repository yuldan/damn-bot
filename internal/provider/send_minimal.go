//go:build minimal

package provider

import (
	"context"

	"github.com/damnbot/damnbot/internal/netx/retry"
)

func (o *OpenAI) send(ctx context.Context, rt *Route, r *Request, est int64, onChunk func(Chunk)) (Response, error) {
	var drops []CarrierDrop
	for {
		drop := o.dropped(rt)
		body, sent, dropped, err := wireOf(rt).Encode(rt, r, drop)
		drops = mergeDrops(drops, dropped)
		if err != nil {
			return Response{CarrierDrops: drops}, &Error{Class: retry.BadRequest, Err: err}
		}
		resp, err := attempt(ctx, o.c, rt, body, est, onChunk)
		resp.CarrierDrops = mergeDrops(drops, resp.CarrierDrops)
		p := rejectedParam(err, r, drop)
		if p != "" {
			o.mu.Lock()
			if o.drop[rt.Ref()] == nil {
				o.drop[rt.Ref()] = map[string]bool{}
			}
			o.drop[rt.Ref()][p] = true
			o.mu.Unlock()
			continue
		}
		if err != nil && len(sent) > 0 && isSignatureInvalid(err) {
			body2, _, dropped2, eerr := wireOf(rt).Encode(rt, withoutCarriers(r), drop)
			if eerr != nil {
				return resp, &Error{Class: retry.BadRequest, Err: eerr}
			}
			retried, rerr := attempt(ctx, o.c, rt, body2, est, onChunk)
			retried.CarrierDrops = mergeDrops(mergeDrops(drops, dropped2), retried.CarrierDrops)
			retried.RejectedCarriers = sent
			return retried, rerr
		}
		return resp, err
	}
}
