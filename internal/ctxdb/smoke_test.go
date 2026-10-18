package ctxdb

import (
	"context"
	"testing"
)

func TestSmoke(t *testing.T) {
	e := newEnv(t)
	s := e.open(100, "tui")
	session(t, s, "A", "")
	appendMsg(t, s, "A", "hello")
	if n := count(t, s, "SELECT count(*) FROM events WHERE session_id='A'"); n != 1 {
		t.Fatalf("events=%d", n)
	}
	checkIntegrity(t, s)
}

func TestSmokePurge(t *testing.T) {
	e := newEnv(t)
	s := e.open(100, "tui")
	session(t, s, "A", "")
	session(t, s, "B", "A")
	p, err := s.PutBlob([]byte("x"), "text/plain")
	must(t, err)
	_, err = s.Append(context.Background(), "B", []*PendingBlob{p}, Event{Kind: "tool_result", Content: "r", Blob: p.Sha})
	must(t, err)
	appendMsg(t, s, "A", "hi")
	must(t, s.ReleaseLease(context.Background(), "A"))
	must(t, s.ReleaseLease(context.Background(), "B"))
	rep, err := s.Purge(context.Background(), PurgeRequest{IDs: []string{"A"}, WithForks: true})
	must(t, err)
	t.Logf("%+v", rep)
	if n := count(t, s, "SELECT count(*) FROM sessions WHERE id IN ('A','B')"); n != 0 {
		t.Fatalf("sessions left %d", n)
	}
	if n := count(t, s, "SELECT count(*) FROM blobs"); n != 0 {
		t.Fatalf("blobs left %d", n)
	}
	checkIntegrity(t, s)
}
