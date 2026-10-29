package ctxdb

import (
	"context"
	"database/sql"
	"testing"
)

// A read connection reserved under mu must survive a drain: detach may close
// the pool afterwards (watch on a maintenance marker, or preWrite), but the
// reserved connection and its rows stay valid until the read ends (I8 open
// order, §11.11.7 draining). Before the reservation this read failed with the
// unclassified "sql: database is closed" (S017 AutoRestoreBlockedWriterTimeout).
func TestReadConnSurvivesDrain(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	inRead, proceed := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.withConn(context.Background(), func(c *sql.Conn) error {
			close(inRead)
			<-proceed
			var n int64
			return getOne(context.Background(), c, "SELECT 1", nil, &n)
		})
	}()
	<-inRead
	s.mu.Lock()
	must(t, s.detach()) // what watch() does when it sees the marker
	s.mu.Unlock()
	close(proceed)
	must(t, <-done)
	// The next read reattaches through the barrier open order.
	if _, err := s.Count(context.Background(), "SELECT count(*) FROM sessions"); err != nil {
		t.Fatalf("read after drain: %v", err)
	}
}
