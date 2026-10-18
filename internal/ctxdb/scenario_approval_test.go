package ctxdb

import (
	"sync"
	"testing"
	"time"
)

func approvalScenarios() []scenario {
	return []scenario{
		{"S030_InboxShortLeaseImportBatch", s030},
		{"S041_ResponseAfterTurnEnd", s041},
		{"S043_StaleAnswerForPreviousRequest", s043},
		{"S050_MCPServeMessagesSendUnderGuards", s050},
		{"S052_WrongScopeOrIdentity", s052},
		{"S053_TwoResponders", s053},
		{"S054_ConsumerCrashBeforeCommit", s054},
		{"S055_RemoteEditNeverExecuted", s055},
		{"S059_EditToDangerousClass", s059},
		{"S060_InvalidRowDeniedConsumed", s060},
		{"S064_CancelBeforeDelivery", s064},
		{"S065_ReplayKeepsRequestID", s065},
		{"S066_TokenRevokedBeforeConsume", s066},
		{"S068_RetryWithoutKeyTwoRequests", s068},
		{"S073_SameKeyDifferentArgs", s073},
	}
}

const owner = "owner"

func pending(t *testing.T, s *Store, sid, turn, rid string, dangerous bool) ApprovalRequest {
	t.Helper()
	req := ApprovalRequest{RequestID: rid, Class: "shell", Dangerous: dangerous, PreviewSha: "p1",
		Channels: []string{"web", "gateway", "mcp_serve"}, ExpiresAt: s.clk.WallMs() + 600_000}
	must(t, s.Write(bg, func(tx *Tx) error {
		if err := tx.StartTurn(sid, turn); err != nil {
			return err
		}
		return tx.RequestApproval(turn, req)
	}))
	return req
}

func token(t *testing.T, s *Store, id, scope, identity string, revoked bool) {
	t.Helper()
	var rev any
	if revoked {
		rev = 1
	}
	must(t, s.Write(bg, func(tx *Tx) error {
		_, err := tx.Exec("INSERT INTO mcp_serve_tokens(id_hash, profile, identity, scope, created_at, revoked_at) VALUES (?, 'p', ?, ?, 0, ?)", id, identity, scope, rev)
		return err
	}))
}

func answer(sid, turn, rid, decision, src, who string) ApprovalResponse {
	r := ApprovalResponse{SessionID: sid, TurnID: turn, RequestID: rid, PreviewSha: "p1", Decision: decision, Source: src, Responder: who}
	if src == "mcp_serve" {
		r.Token = "tok-" + who
	}
	return r
}

// #30: approval inbox from a lease-less process; `undo` short lease; import
// lease per batch released afterwards.
func s030(t *testing.T) {
	e := newEnv(t)
	tui, mcp, cli := e.open(1, "tui"), e.open(2, "mcp_serve"), e.open(3, "cli")
	session(t, tui, "S", "")
	pending(t, tui, "S", "T1", "r1", false)
	token(t, tui, "tok-"+owner, "approve", owner, false)
	must(t, mcp.RespondApproval(bg, answer("S", "T1", "r1", "allow_once", "mcp_serve", owner)))
	d, err := tui.ConsumeApproval(bg, "S", "T1", owner, nil)
	must(t, err)
	if d.Decision != "allow_once" {
		t.Fatalf("decision %+v", d)
	}
	_, err = cli.AcquireLease(bg, "S") // undo from shell while TUI holds the session
	wantErr(t, err, ErrSessionBusy)
	must(t, cli.Write(bg, func(tx *Tx) error { // import batch: lease per batch
		if _, err := tx.CreateSession(Session{ID: "I1", Origin: "import"}); err != nil {
			return err
		}
		_, err := tx.AppendEvent("I1", Event{Kind: "imported", Content: "old"})
		return err
	}))
	must(t, cli.ReleaseLease(bg, "I1"))
	_, err = tui.AcquireLease(bg, "I1")
	must(t, err)
}

// #41: answer for a turn that already ended → approval_not_pending.
func s041(t *testing.T) {
	e := newEnv(t)
	tui, srv := e.open(1, "tui"), e.open(2, "serve")
	session(t, tui, "S", "")
	pending(t, tui, "S", "T1", "r1", false)
	must(t, tui.Write(bg, func(tx *Tx) error { return tx.EndTurn("S", "T1", "cancelled") }))
	wantErr(t, srv.RespondApproval(bg, answer("S", "T1", "r1", "allow_once", "web", owner)), ErrApprovalNotPending)
}

// #43: a late/double answer for request #1 never answers request #2.
func s043(t *testing.T) {
	e := newEnv(t)
	tui, srv := e.open(1, "tui"), e.open(2, "serve")
	session(t, tui, "S", "")
	pending(t, tui, "S", "T1", "r1", false)
	must(t, srv.RespondApproval(bg, answer("S", "T1", "r1", "deny", "web", owner)))
	_, err := tui.ConsumeApproval(bg, "S", "T1", owner, nil)
	must(t, err)
	must(t, tui.Write(bg, func(tx *Tx) error {
		return tx.RequestApproval("T1", ApprovalRequest{RequestID: "r2", PreviewSha: "p1", Channels: []string{"web"}, ExpiresAt: e.clk.WallMs() + 60_000})
	}))
	wantErr(t, srv.RespondApproval(bg, answer("S", "T1", "r1", "allow_once", "web", owner)), ErrApprovalNotPending)
	if count(t, tui, "SELECT count(*) FROM approval_responses") != 0 {
		t.Fatal("stale answer stored")
	}
}

// #50: mcp serve messages_send: short session + lease + journal + inbox,
// only while a serve process exists (module_unavailable otherwise).
func s050(t *testing.T) {
	e := newEnv(t)
	mcp := e.open(1, "mcp_serve")
	if hasServe(mcp) {
		t.Fatal("no serve running yet")
	}
	srv := e.open(2, "serve")
	if !hasServe(mcp) {
		t.Fatal("serve not visible in holders")
	}
	_, err := mcp.CreateSession(bg, Session{ID: "M", Origin: "headless"})
	must(t, err)
	pending(t, mcp, "M", "T1", "r1", true)
	must(t, srv.RespondApproval(bg, answer("M", "T1", "r1", "allow_once", "web", owner)))
	d, err := mcp.ConsumeApproval(bg, "M", "T1", owner, nil)
	must(t, err)
	ev := appendMsg(t, mcp, "M", "send")
	must(t, mcp.Write(bg, func(tx *Tx) error {
		_, err := tx.EnqueueDelivery(Delivery{IdemKey: "mcp:id:k", Platform: "telegram", Chat: "c", Body: "hi", EventID: ev})
		return err
	}))
	if d.Decision != "allow_once" || count(t, srv, "SELECT count(*) FROM session_leases WHERE session_id = 'M' AND owner_kind = 'mcp_serve'") != 1 {
		t.Fatal("messages_send flow not under lease")
	}
}

func hasServe(s *Store) bool {
	for _, h := range s.holders() {
		if h.Kind == "serve" {
			return true
		}
	}
	return false
}

// #52: token with scope `send`, or another identity → deny{scope}.
func s052(t *testing.T) {
	e := newEnv(t)
	tui := e.open(1, "tui")
	session(t, tui, "S", "")
	token(t, tui, "tok-x", "send", "x", false)
	token(t, tui, "tok-y", "approve", "someone-else", false)
	for i, who := range []string{"x", "y"} {
		rid := "r" + itoa(int64(i))
		pending(t, tui, "S", "T"+rid, rid, false)
		must(t, tui.RespondApproval(bg, answer("S", "T"+rid, rid, "allow_once", "mcp_serve", who)))
		d, err := tui.ConsumeApproval(bg, "S", "T"+rid, owner, nil)
		must(t, err)
		if d.Decision != "deny" || d.Reason != "scope" {
			t.Fatalf("%s → %+v", who, d)
		}
	}
}

// #53: two legitimate responders at once → the first wins (UNIQUE).
func s053(t *testing.T) {
	e := newEnv(t)
	tui, a, b := e.open(1, "tui"), e.open(2, "serve"), e.open(3, "serve")
	session(t, tui, "S", "")
	pending(t, tui, "S", "T1", "r1", false)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, s := range []*Store{a, b} {
		src := []string{"gateway", "web"}[i]
		wg.Go(func() { errs[i] = s.RespondApproval(bg, answer("S", "T1", "r1", "allow_once", src, owner)) })
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) {
		t.Fatalf("want exactly one success: %v / %v", errs[0], errs[1])
	}
}

// #54: consumer dies after reading the answer, before COMMIT → the answer
// stays; the new lease holder consumes it exactly once.
func s054(t *testing.T) {
	e := newEnv(t)
	tui, srv := e.open(1, "tui"), e.open(2, "serve")
	session(t, tui, "S", "")
	pending(t, tui, "S", "T1", "r1", false)
	ed := answer("S", "T1", "r1", "edit", "web", owner)
	ed.EditedArgs = `{"cmd":"ls"}`
	must(t, srv.RespondApproval(bg, ed))
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = tui.ConsumeApproval(bg, "S", "T1", owner, func(string) bool { goexit(); return true })
	}()
	<-done
	if count(t, srv, "SELECT count(*) FROM approval_responses") != 1 {
		t.Fatal("answer lost by the crashed consumer")
	}
	e.procs.kill(1)
	_, err := srv.AcquireLease(bg, "S")
	must(t, err)
	d, err := srv.ConsumeApproval(bg, "S", "T1", owner, func(string) bool { return true })
	must(t, err)
	if d.Decision != "edit" || count(t, srv, "SELECT count(*) FROM approvals") != 1 || count(t, srv, "SELECT count(*) FROM approval_responses") != 0 {
		t.Fatalf("consumed %+v", d)
	}
}

// #55: remote edit → new request with new preview, never executed; a
// mcp_serve edit is rejected by the CHECK.
func s055(t *testing.T) {
	e := newEnv(t)
	tui := e.open(1, "tui")
	session(t, tui, "S", "")
	pending(t, tui, "S", "T1", "r1", false)
	r := answer("S", "T1", "r1", "edit", "web", owner)
	r.EditedArgs = `{"cmd":"rm -rf /"}`
	must(t, tui.RespondApproval(bg, r))
	d, err := tui.ConsumeApproval(bg, "S", "T1", owner, func(string) bool { return true })
	must(t, err)
	if d.Decision != "edit" || d.NewRequestID == "" || d.NewRequestID == "r1" {
		t.Fatalf("decision %+v", d)
	}
	var state string
	must(t, tui.get(bg, "SELECT state FROM turn_journal WHERE turn_id = 'T1'", nil, &state))
	if state != "awaiting_approval" {
		t.Fatal("edited args executed without a new approval")
	}
	token(t, tui, "tok-"+owner, "approve", owner, false)
	m := answer("S", "T1", d.NewRequestID, "edit", "mcp_serve", owner)
	m.EditedArgs = "{}"
	if err := tui.RespondApproval(bg, m); err == nil {
		t.Fatal("mcp_serve edit accepted")
	}
}

// #59: an edit by a non-owner on a dangerous class → deny; by the owner →
// new request (P26 class re-classified by the caller).
func s059(t *testing.T) {
	e := newEnv(t)
	tui := e.open(1, "tui")
	session(t, tui, "S", "")
	pending(t, tui, "S", "T1", "r1", true)
	r := answer("S", "T1", "r1", "edit", "gateway", "member")
	r.EditedArgs = `{"cmd":"git push --force"}`
	must(t, tui.RespondApproval(bg, r))
	d, err := tui.ConsumeApproval(bg, "S", "T1", owner, func(string) bool { return true })
	must(t, err)
	if d.Decision != "deny" || d.Reason != "unauthorized" {
		t.Fatalf("decision %+v", d)
	}
}

// #60: an invalid row for a pending request → deny recorded, consumed.
func s060(t *testing.T) {
	e := newEnv(t)
	tui := e.open(1, "tui")
	session(t, tui, "S", "")
	pending(t, tui, "S", "T1", "r1", false)
	r := answer("S", "T1", "r1", "allow_once", "web", owner)
	r.PreviewSha = "different"
	must(t, tui.RespondApproval(bg, r))
	d, err := tui.ConsumeApproval(bg, "S", "T1", owner, nil)
	must(t, err)
	if d.Decision != "deny" || count(t, tui, "SELECT count(*) FROM approval_responses") != 0 ||
		count(t, tui, "SELECT count(*) FROM approvals WHERE decision = 'deny'") != 1 {
		t.Fatalf("decision %+v", d)
	}
}

// #64: client cancel before the decision → deny{cancelled}, no delivery;
// a retry with the same idempotency key never sends twice.
func s064(t *testing.T) {
	e := newEnv(t)
	mcp := e.open(1, "mcp_serve")
	session(t, mcp, "M", "")
	pending(t, mcp, "M", "T1", "r1", false)
	must(t, mcp.CancelApproval(bg, "M", "T1"))
	if count(t, mcp, "SELECT count(*) FROM gateway_deliveries") != 0 ||
		count(t, mcp, "SELECT count(*) FROM approvals WHERE decision = 'deny'") != 1 {
		t.Fatal("cancel did not deny before delivery")
	}
	ev := appendMsg(t, mcp, "M", "x")
	var ids [2]int64
	for i := range ids {
		must(t, mcp.Write(bg, func(tx *Tx) (err error) {
			ids[i], err = tx.EnqueueDelivery(Delivery{IdemKey: "mcp:id:k1", Platform: "t", Chat: "c", Body: "hi", EventID: ev})
			return err
		}))
	}
	if ids[0] != ids[1] {
		t.Fatal("retry created a second delivery")
	}
}

// #65: request replayed after restart keeps request_id; any listed channel
// may answer once.
func s065(t *testing.T) {
	e := newEnv(t)
	tui := e.open(1, "tui")
	session(t, tui, "S", "")
	pending(t, tui, "S", "T1", "r1", false)
	e.procs.kill(1)
	srv := e.open(2, "serve")
	_, err := srv.AcquireLease(bg, "S")
	must(t, err)
	var rid string
	must(t, srv.get(bg, "SELECT json_extract(approval_request, '$.request_id') FROM turn_journal WHERE turn_id = 'T1'", nil, &rid))
	if rid != "r1" {
		t.Fatal("request_id changed on replay")
	}
	must(t, srv.RespondApproval(bg, answer("S", "T1", "r1", "allow_once", "gateway", owner)))
	d, err := srv.ConsumeApproval(bg, "S", "T1", owner, nil)
	must(t, err)
	if d.Decision != "allow_once" {
		t.Fatalf("decision %+v", d)
	}
}

// #66: token revoked between INSERT and consumption → deny{scope}.
func s066(t *testing.T) {
	e := newEnv(t)
	tui, mcp := e.open(1, "tui"), e.open(2, "mcp_serve")
	session(t, tui, "S", "")
	token(t, tui, "tok-"+owner, "approve", owner, false)
	pending(t, tui, "S", "T1", "r1", false)
	must(t, mcp.RespondApproval(bg, answer("S", "T1", "r1", "allow_once", "mcp_serve", owner)))
	must(t, tui.Write(bg, func(tx *Tx) error {
		_, err := tx.Exec("UPDATE mcp_serve_tokens SET revoked_at = 1")
		return err
	}))
	d, err := tui.ConsumeApproval(bg, "S", "T1", owner, nil)
	must(t, err)
	if d.Decision != "deny" || d.Reason != "scope" {
		t.Fatalf("decision %+v", d)
	}
}

// #68: retry without idempotency_key = a new request; if both approved,
// two separately approved messages (documented residue).
func s068(t *testing.T) {
	e := newEnv(t)
	mcp, srv := e.open(1, "mcp_serve"), e.open(2, "serve")
	session(t, mcp, "M", "")
	for i, rid := range []string{"r1", "r2"} {
		turn := "T" + itoa(int64(i))
		pending(t, mcp, "M", turn, rid, false)
		must(t, srv.RespondApproval(bg, answer("M", turn, rid, "allow_once", "web", owner)))
		_, err := mcp.ConsumeApproval(bg, "M", turn, owner, nil)
		must(t, err)
		ev := appendMsg(t, mcp, "M", "send")
		must(t, mcp.Write(bg, func(tx *Tx) error {
			_, err := tx.EnqueueDelivery(Delivery{IdemKey: "mcp:id:" + newID(), Platform: "t", Chat: "c", Body: "hi", EventID: ev})
			return err
		}))
	}
	if count(t, srv, "SELECT count(*) FROM gateway_deliveries")+count(t, srv, "SELECT count(*) FROM approvals WHERE decision = 'allow_once'") != 4 {
		t.Fatal("expected two requests, two approvals, two deliveries")
	}
}

// #73: same idempotency key with different arguments → idempotency_conflict.
func s073(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "mcp_serve")
	session(t, s, "M", "")
	ev := appendMsg(t, s, "M", "x")
	enq := func(body string) error {
		return s.Write(bg, func(tx *Tx) error {
			_, err := tx.EnqueueDelivery(Delivery{IdemKey: "mcp:id:k", Platform: "t", Chat: "c", Body: body, EventID: ev})
			return err
		})
	}
	must(t, enq("hello"))
	wantErr(t, enq("goodbye"), ErrIdempotencyConflict)
	e.clk.Advance(time.Second)
	if count(t, s, "SELECT count(*) FROM gateway_deliveries") != 1 {
		t.Fatal("conflicting retry created a delivery")
	}
}
