// Package mcp is the lazy MCP client module (SPEC §9.6). It speaks stdio and
// Streamable HTTP through internal/netx, caches tools per (server, config_sha),
// and proves that every privileged need goes through internal/modules/host:
// approvals, budget, the child environment, and the clock. Tool pinning,
// OAuth and `mcp serve` belong to later F5 slices.
package mcp

import (
	"context"
	"errors"
	"fmt"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// samplingPurpose is the ledger purpose of MCP sampling (SPEC §9.6).
const samplingPurpose = "delegate"

// Sentinel errors of the MCP seam.
var (
	// ErrSamplingDisabled is returned when mcp.allow_sampling is false.
	ErrSamplingDisabled = errors.New("mcp: sampling disabled")
	// ErrSamplingUnavailable is returned when the core has no completer.
	ErrSamplingUnavailable = errors.New("mcp: sampling unavailable")
	// ErrSamplingInvalid is a sampling request outside the contract.
	ErrSamplingInvalid = errors.New("mcp: invalid sampling request")
)

// Sampling is the guarded sampling entry point. It is the only way a server's
// sampling request reaches an LLM: opt-in first, then the budget guard, then
// the completer.
type Sampling struct {
	enabled bool
	caps    host.Capabilities
	llm     host.Completer
}

// NewSampling builds the sampling seam for one host. enabled is
// `mcp.allow_sampling`; a disabled or unwired host fails closed.
func NewSampling(h host.Host, enabled bool) *Sampling {
	s := &Sampling{enabled: enabled, caps: host.CapabilitiesOf(h)}
	if h != nil {
		s.llm = h.LLM(samplingPurpose)
	}
	return s
}

// Complete answers one server sampling request. A missing opt-in, a missing
// completer, an unbounded max_output_tokens, or a budget denial stops the call
// before the provider sees it.
func (s *Sampling) Complete(ctx context.Context, req host.Request) (host.Response, error) {
	if s == nil || !s.enabled {
		return host.Response{}, ErrSamplingDisabled
	}
	if s.llm == nil {
		return host.Response{}, ErrSamplingUnavailable
	}
	if req.MaxOutputTokens <= 0 {
		return host.Response{}, fmt.Errorf("%w: max_output_tokens must be positive", ErrSamplingInvalid)
	}
	budget := host.BudgetRequest{
		Purpose:         samplingPurpose,
		Model:           req.Model,
		MaxOutputTokens: req.MaxOutputTokens,
	}
	if err := s.caps.Budget.Check(ctx, budget); err != nil {
		return host.Response{}, fmt.Errorf("mcp: sampling budget: %w", err)
	}
	resp, err := s.llm.Complete(ctx, req)
	if err != nil {
		return host.Response{}, fmt.Errorf("mcp: sampling: %w", err)
	}
	return resp, nil
}
