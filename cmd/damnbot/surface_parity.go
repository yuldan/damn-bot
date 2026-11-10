//go:build !minimal

// The server-request contract and its generated doc (GAP-UX-09) are a
// full-build surface; the minimal binary omits the contract tables.

package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// ServerRequest is one loop request that needs a frontend answer (GAP-UX-09).
type ServerRequest string

const (
	ReqApproval    ServerRequest = "approval"
	ReqAskUser     ServerRequest = "ask_user"
	ReqSecretInput ServerRequest = "secret_input"
	ReqPlanReview  ServerRequest = "plan_review"
	ReqBudgetRaise ServerRequest = "budget_raise"
	ReqUndoConfirm ServerRequest = "undo_confirm"
)

// Frontend is one of the five surfaces that consume the loop.
type Frontend string

const (
	FrontTUI      Frontend = "tui"
	FrontWeb      Frontend = "web"
	FrontACP      Frontend = "acp"
	FrontGateway  Frontend = "gateway"
	FrontHeadless Frontend = "headless"
)

// SurfaceMode is either a real renderer or a defined fallback; an empty
// fallback is never allowed.
type SurfaceMode string

const (
	SurfaceRender   SurfaceMode = "render"
	SurfaceFallback SurfaceMode = "fallback"
)

// SurfaceRow is one cell of the generated support table.
type SurfaceRow struct {
	Request  ServerRequest
	Frontend Frontend
	Mode     SurfaceMode
	Fallback string // reject:<reason> | forward:owner
}

// serverRequests is the closed request list of SPEC §13.2/ADR-006.
func serverRequests() []ServerRequest {
	return []ServerRequest{ReqApproval, ReqAskUser, ReqSecretInput, ReqPlanReview, ReqBudgetRaise, ReqUndoConfirm}
}

// frontends is the closed frontend list.
func frontends() []Frontend {
	return []Frontend{FrontTUI, FrontWeb, FrontACP, FrontGateway, FrontHeadless}
}

// surfaceContract is the single source of the per-frontend table. A missing
// cell is a code bug caught by [checkSurfaceParity], not a silent
// unsupported_command.
func surfaceContract() []SurfaceRow {
	r := SurfaceRender
	rows := []SurfaceRow{
		{ReqApproval, FrontTUI, r, ""},
		{ReqApproval, FrontWeb, r, ""},
		{ReqApproval, FrontACP, r, ""},
		{ReqApproval, FrontGateway, r, ""},
		{ReqApproval, FrontHeadless, SurfaceFallback, "reject:approval_required"},

		{ReqAskUser, FrontTUI, r, ""},
		{ReqAskUser, FrontWeb, r, ""},
		{ReqAskUser, FrontACP, SurfaceFallback, "reject:ask_user_unavailable"},
		{ReqAskUser, FrontGateway, r, ""},
		{ReqAskUser, FrontHeadless, SurfaceFallback, "reject:ask_user_unavailable"},

		{ReqSecretInput, FrontTUI, r, ""},
		{ReqSecretInput, FrontWeb, SurfaceFallback, "reject:secret_input_local_only"},
		{ReqSecretInput, FrontACP, SurfaceFallback, "reject:secret_input_local_only"},
		{ReqSecretInput, FrontGateway, SurfaceFallback, "reject:secret_input_local_only"},
		{ReqSecretInput, FrontHeadless, SurfaceFallback, "reject:secret_input_local_only"},

		{ReqPlanReview, FrontTUI, r, ""},
		{ReqPlanReview, FrontWeb, r, ""},
		{ReqPlanReview, FrontACP, r, ""},
		{ReqPlanReview, FrontGateway, r, ""},
		{ReqPlanReview, FrontHeadless, SurfaceFallback, "reject:plan_review_unavailable"},

		{ReqBudgetRaise, FrontTUI, r, ""},
		{ReqBudgetRaise, FrontWeb, r, ""},
		{ReqBudgetRaise, FrontACP, r, ""},
		{ReqBudgetRaise, FrontGateway, r, ""},
		{ReqBudgetRaise, FrontHeadless, SurfaceFallback, "forward:owner"},

		{ReqUndoConfirm, FrontTUI, r, ""},
		{ReqUndoConfirm, FrontWeb, r, ""},
		{ReqUndoConfirm, FrontACP, SurfaceFallback, "reject:undo_confirm_unavailable"},
		{ReqUndoConfirm, FrontGateway, r, ""},
		{ReqUndoConfirm, FrontHeadless, SurfaceFallback, "reject:undo_confirm_unavailable"},
	}
	return rows
}

// updateKinds is the closed stream vocabulary every frontend must handle.
func updateKinds() []event.UpdateKind {
	return []event.UpdateKind{
		event.UpdateTextDelta, event.UpdateToolCallStart, event.UpdateToolCallArgs,
		event.UpdateToolCallEnd, event.UpdateToolResult, event.UpdateUsage,
		event.UpdateNotice, event.UpdateStop,
	}
}

// checkSurfaceParity returns an error for a missing cell or an empty
// fallback: the generator behind TestSurfaceParity.
func checkSurfaceParity() error {
	got := map[[2]string]SurfaceRow{}
	for _, row := range surfaceContract() {
		if row.Mode != SurfaceRender && row.Mode != SurfaceFallback {
			return fmt.Errorf("%s/%s: unknown mode %q", row.Frontend, row.Request, row.Mode)
		}
		if row.Mode == SurfaceFallback && row.Fallback == "" {
			return fmt.Errorf("%s/%s: fallback without a reason", row.Frontend, row.Request)
		}
		got[[2]string{string(row.Request), string(row.Frontend)}] = row
	}
	for _, req := range serverRequests() {
		for _, front := range frontends() {
			if _, ok := got[[2]string{string(req), string(front)}]; !ok {
				return fmt.Errorf("missing %s/%s", front, req)
			}
		}
	}
	if len(updateKinds()) == 0 {
		return fmt.Errorf("no update kinds")
	}
	return nil
}

// surfaceDoc renders docs/SURFACE-PARITY.md from the table. TestSurfaceDocSync
// fails when the checked-in copy drifts.
func surfaceDoc() string {
	var b strings.Builder
	b.WriteString("# Surface parity\n\n<!-- Code generated from cmd/damnbot/surface_parity.go; DO NOT EDIT. -->\n\n")
	b.WriteString("Server requests × frontends. A `fallback` cell names the defined\n")
	b.WriteString("alternative, so no frontend silently answers `unsupported_command`.\n\n")
	b.WriteString("| Request | " + strings.Join(frontendNames(), " | ") + " |\n")
	b.WriteString("|---|" + strings.Repeat("---|", len(frontends())) + "\n")
	byKey := map[[2]string]SurfaceRow{}
	for _, row := range surfaceContract() {
		byKey[[2]string{string(row.Request), string(row.Frontend)}] = row
	}
	for _, req := range serverRequests() {
		b.WriteString("| `" + string(req) + "` |")
		for _, front := range frontends() {
			row := byKey[[2]string{string(req), string(front)}]
			if row.Mode == SurfaceRender {
				b.WriteString(" render |")
			} else {
				b.WriteString(" fallback: `" + row.Fallback + "` |")
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("\n## Stream updates\n\nEvery frontend renders or has a defined fallback for the closed\nupdate vocabulary: ")
	names := make([]string, 0, len(updateKinds()))
	for _, k := range updateKinds() {
		names = append(names, "`"+string(k)+"`")
	}
	sort.Strings(names)
	b.WriteString(strings.Join(names, ", ") + ".\n")
	return b.String()
}

func frontendNames() []string {
	out := make([]string, 0, len(frontends()))
	for _, f := range frontends() {
		out = append(out, string(f))
	}
	return out
}
