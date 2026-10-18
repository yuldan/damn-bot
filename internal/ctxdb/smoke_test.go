package ctxdb

import "testing"

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
