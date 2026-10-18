package ctxdb

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func gatewayScenarios() []scenario {
	return []scenario{
		{"S010_InboundToLabelledSession", s010},
		{"S011_OutboxSkipsLabelledSession", s011},
		{"S069_QueuedInboundSurvivesRestart", s069},
		{"S070_SpoolIngestThenWorker", s070},
		{"S072_SpoolTruncatedLineNotAcked", s072},
		{"S075_CrashBetweenPollerAndWorker", s075},
		{"S076_WorkerVsPurgeLabel", s076},
		{"S077_InboundUpdateGuard", s077},
		{"S079_InvalidPayloadNotSilent", s079},
		{"S080_CrashAfterWorkerCommitAnswered", s080},
		{"S081_PerChatOrderInsertion", s081},
		{"S082_ChatSessionBusyNotice", s082},
		{"S083_PurgeInboundAttachment", s083},
		{"S084_SpoolDuringDrainBarrier", s084},
	}
}

func inbound(ext, chat, text string, att ...string) Inbound {
	as := make([]map[string]string, 0, len(att))
	for _, a := range att {
		as = append(as, map[string]string{"blob_sha": a})
	}
	return Inbound{Platform: "telegram", ExternalID: ext, ChatID: chat, SenderID: "u1",
		Payload: jsonOf(map[string]any{"text": text, "attachments": as, "kind": "text"})}
}

func ingest(t *testing.T, s *Store, msgs ...Inbound) {
	t.Helper()
	must(t, s.IngestInbound(bg, msgs, "telegram.offset", itoa(int64(len(msgs)))))
}

// finishTurn ends the worker's turn and releases the chat session lease.
func finishTurn(t *testing.T, s *Store, p Processed) {
	t.Helper()
	must(t, s.Write(bg, func(tx *Tx) error { return tx.EndTurn(p.Session, p.Turn, "end_turn") }))
	must(t, s.ReleaseLease(bg, p.Session))
}

// #10: inbound to a chat whose session is labelled → new session + exactly
// one system reply per chat per txid; the message is never dropped.
func s010(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "serve")
	ingest(t, s, inbound("u:1", "c1", "first"))
	p1, err := s.ProcessInbound(bg, "telegram", "c1")
	must(t, err)
	finishTurn(t, s, p1)
	purgePhase0(t, s, p1.Session)
	ingest(t, s, inbound("u:2", "c1", "second"), inbound("u:3", "c1", "third"))
	p2, err := s.ProcessInbound(bg, "telegram", "c1")
	must(t, err)
	finishTurn(t, s, p2)
	p3, err := s.ProcessInbound(bg, "telegram", "c1")
	must(t, err)
	if !p2.NewSession || p3.NewSession || p3.Session != p2.Session || p2.Session == p1.Session {
		t.Fatalf("sessions %+v %+v %+v", p1, p2, p3)
	}
	if n := count(t, s, "SELECT count(*) FROM gateway_deliveries WHERE event_id IS NULL AND idem_key LIKE 'sys:purge_notice:%'"); n != 1 {
		t.Fatalf("system notices = %d", n)
	}
}

// #11: outbox never sends a delivery of a labelled session.
func s011(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "serve")
	session(t, s, "S", "")
	ev := appendMsg(t, s, "S", "answer")
	must(t, s.Write(bg, func(tx *Tx) error {
		_, err := tx.EnqueueDelivery(Delivery{IdemKey: "k1", Platform: "telegram", Chat: "c1", Body: "answer", EventID: ev})
		return err
	}))
	must(t, s.ReleaseLease(bg, "S"))
	purgePhase0(t, s, "S")
	_, err := s.ClaimDelivery(bg)
	wantErr(t, err, ErrNotFound)
	err = s.Write(bg, func(tx *Tx) error {
		_, err := tx.Exec("UPDATE gateway_deliveries SET status = 'sending' WHERE idem_key = 'k1'")
		return err
	})
	wantErr(t, err, ErrSessionPurging)
}

// #69: committed, queued messages survive serve exit/crash with content.
func s069(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "serve")
	var msgs []Inbound
	for i := range 5 {
		msgs = append(msgs, inbound("u:"+itoa(int64(i)), "c1", "msg "+itoa(int64(i))))
	}
	ingest(t, s, msgs...)
	must(t, s.Close())
	e.procs.kill(1)
	s2 := e.open(2, "serve")
	for i := range 5 {
		p, err := s2.ProcessInbound(bg, "telegram", "c1")
		must(t, err)
		var content string
		must(t, s2.get(bg, "SELECT content FROM events WHERE id = ?", []any{p.EventID}, &content))
		if content != "msg "+itoa(int64(i)) {
			t.Fatalf("message %d = %q", i, content)
		}
		finishTurn(t, s2, p)
	}
	if count(t, s2, "SELECT count(*) FROM gateway_inbound WHERE payload IS NOT NULL") != 0 {
		t.Fatal("payload kept after processing")
	}
}

// #70: spool → ingest (file removed after COMMIT) → worker.
func s070(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "serve")
	must(t, s.SpoolInbound(1, inbound("b:1", "c1", "via bridge")))
	n, err := s.IngestSpool(bg)
	must(t, err)
	ents, _ := os.ReadDir(runPath(e.dir, "inbound_spool"))
	if n != 1 || len(ents) != 0 {
		t.Fatalf("ingested %d, files left %d", n, len(ents))
	}
	p, err := s.ProcessInbound(bg, "telegram", "c1")
	must(t, err)
	if p.EventID == 0 {
		t.Fatal("no user_message")
	}
}

// #72: a line cut before fsync (never acked) is ignored; fsynced lines are
// ingested exactly once even if ingested twice.
func s072(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "serve")
	must(t, s.SpoolInbound(1, inbound("w:1", "c1", "acked")))
	must(t, s.SpoolInbound(1, inbound("w:1", "c1", "acked")))
	ents, _ := os.ReadDir(runPath(e.dir, "inbound_spool"))
	f, err := os.OpenFile(filepath.Join(runPath(e.dir, "inbound_spool"), ents[0].Name()), os.O_APPEND|os.O_WRONLY, 0o600)
	must(t, err)
	_, err = f.WriteString(`{"platform":"telegram","external_id":"w:2","chat_`)
	must(t, err)
	must(t, f.Close())
	_, err = s.IngestSpool(bg)
	must(t, err)
	if count(t, s, "SELECT count(*) FROM gateway_inbound") != 1 {
		t.Fatal("want exactly one ingested row")
	}
}

// #75: crash between the poller COMMIT and the worker COMMIT, repeated:
// 0 lost, 0 duplicated.
func s075(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "serve")
	pid := 1
	const n = 100
	for i := range n {
		ingest(t, s, inbound("u:"+itoa(int64(i)), "c1", "m"+itoa(int64(i))))
		if i%3 == 0 { // crash before the worker ran
			must(t, s.Close())
			e.procs.kill(pid)
			pid++
			s = e.open(pid, "serve")
		}
		for {
			p, err := s.ProcessInbound(bg, "telegram", "c1")
			if err != nil {
				wantErr(t, err, ErrNotFound)
				break
			}
			finishTurn(t, s, p)
		}
	}
	if got := count(t, s, "SELECT count(*) FROM events WHERE kind = 'user_message'"); got != n {
		t.Fatalf("user_message = %d, want %d", got, n)
	}
	if count(t, s, "SELECT count(*) FROM (SELECT content FROM events WHERE kind = 'user_message' GROUP BY content HAVING count(*) > 1)") != 0 {
		t.Fatal("duplicate user_message")
	}
}

// #76: worker vs purge labelling the chat session → new session, row kept.
func s076(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "serve")
	ingest(t, s, inbound("u:1", "c1", "one"))
	p1, err := s.ProcessInbound(bg, "telegram", "c1")
	must(t, err)
	finishTurn(t, s, p1)
	ingest(t, s, inbound("u:2", "c1", "two"))
	purgePhase0(t, s, p1.Session)
	p2, err := s.ProcessInbound(bg, "telegram", "c1")
	must(t, err)
	if !p2.NewSession || count(t, s, "SELECT count(*) FROM events WHERE session_id = ? AND content = 'two'", p2.Session) != 1 {
		t.Fatal("queued message not routed to a new session")
	}
}

// #77: the only legal UPDATE is (event_id NULL → value, payload → NULL).
func s077(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "serve")
	session(t, s, "S", "")
	ev := appendMsg(t, s, "S", "x")
	ingest(t, s, inbound("u:1", "c1", "x"))
	for _, q := range []string{
		"UPDATE gateway_inbound SET event_id = ?1",
		"UPDATE gateway_inbound SET payload = '{}' WHERE ?1 = ?1",
		"UPDATE gateway_inbound SET chat_id = 'other', event_id = ?1, payload = NULL",
		"DELETE FROM gateway_inbound WHERE ?1 = ?1",
	} {
		if err := s.Write(bg, func(tx *Tx) error { _, err := tx.Exec(q, ev); return err }); err == nil {
			t.Fatalf("%s accepted", q)
		}
	}
	must(t, s.Write(bg, func(tx *Tx) error {
		_, err := tx.Exec("UPDATE gateway_inbound SET event_id = ?, payload = NULL", ev)
		return err
	}))
}

// #79: invalid payload (normalise/redact/encrypt bug) fails loudly; the
// offset does not advance (AF-01 fix: ON CONFLICT DO NOTHING).
func s079(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "serve")
	ingest(t, s, inbound("u:1", "c1", "ok"))
	bad := []Inbound{{Platform: "telegram", ExternalID: "u:2", ChatID: "c1", SenderID: "u1", Payload: "base64:AAAA"},
		{Platform: "telegram", ExternalID: "u:3", ChatID: "c1", SenderID: "u1"}}
	for _, m := range bad {
		wantErr(t, s.IngestInbound(bg, []Inbound{m}, "telegram.offset", "99"), ErrInboundInvalid)
	}
	var off string
	must(t, s.get(bg, "SELECT value FROM module_state WHERE module_id = 'gateway' AND key = 'telegram.offset'", nil, &off))
	if off != "1" || count(t, s, "SELECT count(*) FROM gateway_inbound") != 1 {
		t.Fatalf("offset %s advanced past an invalid message", off)
	}
}

// #80: crash after the worker COMMIT: turn_start + journal are in the same
// transaction (AF-02 fix), so recovery sees the turn and answers it.
func s080(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "serve")
	ingest(t, s, inbound("u:1", "c1", "please answer"))
	p, err := s.ProcessInbound(bg, "telegram", "c1")
	must(t, err)
	if count(t, s, "SELECT count(*) FROM turn_journal WHERE turn_id = ? AND state = 'running'", p.Turn) != 1 {
		t.Fatal("no journal in the worker transaction")
	}
	must(t, s.Close())
	e.procs.kill(1)
	s2 := e.open(2, "serve")
	if count(t, s2, "SELECT count(*) FROM turn_journal WHERE turn_id = ? AND state = 'resumed'", p.Turn) != 1 {
		t.Fatal("message left unanswered after crash")
	}
}

// #81: per-chat order = insertion order, not received_at/external_id text.
func s081(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "serve")
	ingest(t, s, inbound("u:999", "c1", "first"), inbound("u:1000", "c1", "second"))
	e.clk.StepWall(-time.Hour)
	ingest(t, s, inbound("u:1001", "c1", "third"))
	for _, want := range []string{"first", "second", "third"} {
		p, err := s.ProcessInbound(bg, "telegram", "c1")
		must(t, err)
		var c string
		must(t, s.get(bg, "SELECT content FROM events WHERE id = ?", []any{p.EventID}, &c))
		if c != want {
			t.Fatalf("got %q, want %q", c, want)
		}
		finishTurn(t, s, p)
	}
}

// #82: chat session open in the TUI → one system reply, row stays queued,
// processed once the TUI releases the lease.
func s082(t *testing.T) {
	e := newEnv(t)
	srv, tui := e.open(1, "serve"), e.open(2, "tui")
	ingest(t, srv, inbound("u:1", "c1", "one"))
	p1, err := srv.ProcessInbound(bg, "telegram", "c1")
	must(t, err)
	finishTurn(t, srv, p1)
	_, err = tui.AcquireLease(bg, p1.Session)
	must(t, err)
	ingest(t, srv, inbound("u:2", "c1", "two"))
	for range 3 {
		_, err = srv.ProcessInbound(bg, "telegram", "c1")
		wantErr(t, err, ErrSessionBusy)
	}
	if n := count(t, srv, "SELECT count(*) FROM gateway_deliveries WHERE idem_key LIKE 'sys:busy_notice:%'"); n != 1 {
		t.Fatalf("busy notices = %d", n)
	}
	must(t, tui.ReleaseLease(bg, p1.Session))
	p2, err := srv.ProcessInbound(bg, "telegram", "c1")
	must(t, err)
	if p2.Session != p1.Session {
		t.Fatal("message moved to another session")
	}
}

// #83: purge of a session with an inbound attachment removes the file in
// the purge transaction (AF-05 fix: step 11 deletes gateway_inbound refs).
func s083(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "serve")
	pb, err := s.PutBlob([]byte("photo bytes"), "image/jpeg")
	must(t, err)
	m := inbound("u:1", "c1", "look", pb.Sha)
	m.Pending = []*PendingBlob{pb}
	ingest(t, s, m)
	p, err := s.ProcessInbound(bg, "telegram", "c1")
	must(t, err)
	finishTurn(t, s, p)
	_, err = s.Purge(bg, PurgeRequest{IDs: []string{p.Session}})
	must(t, err)
	if _, err := os.Stat(s.blobPath(pb.Sha)); !os.IsNotExist(err) {
		t.Fatal("attachment file left after purge")
	}
	if count(t, s, "SELECT count(*) FROM blob_refs WHERE sha256 = ?", pb.Sha)+count(t, s, "SELECT count(*) FROM blobs WHERE sha256 = ?", pb.Sha) != 0 {
		t.Fatal("attachment rows left after purge")
	}
	assertNoSessionRefs(t, s, p.Session)
}

// #84: a push without redelivery (Discord WS) during a drain barrier is
// spooled instead of lost (AF-06 fix: spool covers every non-redelivered inbound).
func s084(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "serve")
	b, err := AcquireBarrier(bg, e.dir, *e.ident(9), reasonRestore, 30, 5*time.Second)
	must(t, err)
	if !MaintenancePending(e.dir) {
		t.Fatal("barrier not visible")
	}
	ev := inbound("ws:1", "c1", "discord event")
	ev.Platform = "discord"
	must(t, s.SpoolInbound(1, ev))
	b.Release()
	_, err = s.IngestSpool(bg)
	must(t, err)
	if count(t, s, "SELECT count(*) FROM gateway_inbound WHERE platform = 'discord'") != 1 {
		t.Fatal("event lost")
	}
}
