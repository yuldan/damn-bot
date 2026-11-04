//go:build !minimal

package main

import (
	"context"
	"math"

	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/provider"
)

// mediaRouteProvider is the optional run-provider seam that names the vision
// route of the chain.
type mediaRouteProvider interface {
	mediaRoute() (model string, vision bool)
}

// wireRunMedia publishes the run's purpose=media completer and route facts to
// the module host. A provider without the seam (an injected test provider)
// leaves the media module fail-closed.
func wireRunMedia(set func(host.Completer, bool, string), prov loop.Provider) {
	if set == nil {
		return
	}
	r, ok := prov.(mediaRouteProvider)
	if !ok {
		return
	}
	model, vision := r.mediaRoute()
	set(mediaCompleter{prov: prov}, vision, model)
}

// mediaCompleter adapts host.Completer onto the run provider with
// purpose=media, so one client, retry policy, price schedule and circuit
// serve both the turn and media analysis.
type mediaCompleter struct {
	prov loop.Provider
}

// Complete maps the module-neutral images/videos onto the run provider. The
// attachment kind is the only place the two channels differ.
func (c mediaCompleter) Complete(ctx context.Context, req host.Request) (host.Response, error) {
	out := &loop.Request{Model: req.Model, MaxTokens: req.MaxOutputTokens}
	out.Messages = make([]loop.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		lm := loop.Message{Role: m.Role, Content: m.Content}
		for _, img := range m.Images {
			lm.Attachments = append(lm.Attachments, loop.Attachment{
				Kind: "image", Name: img.Name, MIME: img.MIME, Data: img.Data,
			})
		}
		out.Messages = append(out.Messages, lm)
	}
	reply, err := purposeStream(ctx, c.prov, provider.PurposeMedia, out)
	if err != nil {
		return host.Response{}, err
	}
	usage := host.Usage{InputTokens: int(reply.Usage.Input), OutputTokens: int(reply.Usage.Output)}
	if reply.Usage.CostUSD != nil {
		usage.CostMicroUSD = int64(math.Round(*reply.Usage.CostUSD * 1e6))
	}
	return host.Response{Content: reply.Content, Usage: usage}, nil
}
