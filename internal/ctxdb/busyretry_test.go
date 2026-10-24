package ctxdb

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// busyStore opens a Store with a short busy_timeout so a contended write fails
// fast, and records the busy retries it observes.
func busyStore(t *testing.T, dir string, pid int, seen *[]string) *Store {
	t.Helper()
	var mu sync.Mutex
	s, err := Open(context.Background(), &Options{
		Dir: dir, Kind: "cli", BusyTimeout: 40 * time.Millisecond,
		Ident: &Ident{Host: "busy", PID: pid, Start: "busy", BootID: "busy"},
		OnBusyRetry: func(phase string, attempt int) {
			mu.Lock()
			defer mu.Unlock()
			*seen = append(*seen, phase)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// holdWriteLock makes another store hold the SQLite write lock until the returned
// release function is called, and reports (via locked) that BEGIN IMMEDIATE has
// succeeded - so a test never races the holder.
func holdWriteLock(ctx context.Context, t *testing.T, holder *Store) (release func(), done chan error) {
	t.Helper()
	var once sync.Once
	locked := make(chan struct{})
	free := make(chan struct{})
	done = make(chan error, 1)
	release = func() { once.Do(func() { close(free) }) }
	go func() {
		done <- holder.Write(ctx, func(*Tx) error {
			close(locked)
			<-free
			return nil
		})
	}()
	<-locked
	return release, done
}

// TestWriterBusyRetry (GAP-REL-03): the BEGIN/COMMIT retry is load-bearing.
//
//   - with the retry enabled the contended write succeeds and the retry is
//     reported (Store.BusyRetries + OnBusyRetry phase "begin");
//   - with the retry disabled (busyRetryMax = 1) the same contention surfaces the
//     typed ErrDBBusy of SPEC §17 P19 - i.e. the success above is the retry's;
//   - a guarded write that is fenced (ErrLeaseFenced) is never retried.
func TestWriterBusyRetry(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	var seen []string
	holder := busyStore(t, dir, 1, &seen)
	waiter := busyStore(t, dir, 2, &seen)
	defer func() {
		must(t, waiter.Close())
		must(t, holder.Close())
	}()
	const sid = "B1"
	if _, err := waiter.CreateSession(ctx, Session{ID: sid, Cwd: "/"}); err != nil {
		t.Fatal(err)
	}
	if _, err := waiter.Append(ctx, sid, nil, Event{Kind: "user_message", Content: "before"}); err != nil {
		t.Fatal(err)
	}

	// Case 1: the lock is held across two busy windows (2 x 40 ms) and released
	// while the third attempt waits, so only the retry can carry the write home.
	release, done := holdWriteLock(ctx, t, holder)
	time.AfterFunc(100*time.Millisecond, release)
	if _, err := waiter.Append(ctx, sid, nil, Event{Kind: "user_message", Content: "retried"}); err != nil {
		t.Fatalf("append with busy retry enabled: %v", err)
	}
	if got := waiter.BusyRetries(); got == 0 {
		t.Fatalf("busy retries = 0, want >= 1 (the write only succeeded because of the retry)")
	}
	if err := <-done; err != nil {
		t.Fatalf("holder: %v", err)
	}
	if len(seen) == 0 {
		t.Fatalf("OnBusyRetry saw no retry, want at least one phase")
	}

	// Case 2: same contention, retry off -> typed db_busy (the outcome SPEC allows).
	release2, done2 := holdWriteLock(ctx, t, holder)
	old := waiter.busyRetryMax
	waiter.busyRetryMax = 1
	if _, err := waiter.Append(ctx, sid, nil, Event{Kind: "user_message", Content: "no retry"}); !errors.Is(err, ErrDBBusy) {
		t.Fatalf("append with retry disabled = %v, want ErrDBBusy", err)
	}
	waiter.busyRetryMax = old
	release2()
	if err := <-done2; err != nil {
		t.Fatalf("holder 2: %v", err)
	}

	// Case 3: a fenced lease is not a busy error and must never be retried.
	before := waiter.BusyRetries()
	waiter.setToken(sid, 0)
	if _, err := waiter.Append(ctx, sid, nil, Event{Kind: "user_message", Content: "fenced"}); !errors.Is(err, ErrLeaseFenced) {
		t.Fatalf("fenced append = %v, want ErrLeaseFenced", err)
	}
	if got := waiter.BusyRetries(); got != before {
		t.Fatalf("busy retries after a fenced write = %d, want %d (fencing is not a busy error)", got, before)
	}
}
