package gateway_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestEdgeCases catalogs the F4 gateway rows owned by this package: one
// platform failing to start never stops its siblings (EC-PLAT-001), the router
// binds a profile to the bot that received the update (EC-PLAT-007), per-chat
// ordering is EC-PLAT-015, the allowlist accepts both wire forms (EC-PLAT-021)
// and a group message needs an explicit mention (EC-PLAT-022).
func TestEdgeCases(t *testing.T) {
	g := phase.Current(t)
	if g.Item(t, "EC-PLAT-001", "F4", "covered") {
		t.Run("EC-PLAT-001", TestRegistryFailureDoesNotStopSiblings)
	}
	if g.Item(t, "EC-PLAT-007", "F4", "covered") {
		t.Run("EC-PLAT-007", TestRouterLocksProfileToBot)
	}
	if g.Item(t, "EC-PLAT-015", "F4", "covered") {
		t.Run("EC-PLAT-015", TestECPLAT015ChatQueueOrdering)
	}
	if g.Item(t, "EC-PLAT-021", "F4", "covered") {
		t.Run("EC-PLAT-021", TestParseAllowlistJSONAndCSV)
	}
	if g.Item(t, "EC-PLAT-022", "F4", "covered") {
		t.Run("EC-PLAT-022", TestGroupMentionPolicy)
	}
}

// TestECPLAT015ChatQueueOrdering proves the dispatcher contract: one chat runs
// its messages strictly in order, while two chats overlap.
func TestECPLAT015ChatQueueOrdering(t *testing.T) {
	t.Run("one chat stays serial and ordered", testChatQueueSerial)
	t.Run("two chats overlap", testChatQueueOverlap)
}

type queueProbe struct {
	mu     sync.Mutex
	order  []int
	active int
	peak   int
}

func (p *queueProbe) enter(i int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.order = append(p.order, i)
	p.active++
	if p.active > p.peak {
		p.peak = p.active
	}
}

func (p *queueProbe) leave() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active--
}

func (p *queueProbe) snapshot() ([]int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]int(nil), p.order...), p.peak
}

func testChatQueueSerial(t *testing.T) {
	q := gateway.NewChatQueue(2)
	defer q.Close()
	var probe queueProbe
	started := make(chan int, 3)
	release := make(chan struct{})
	for i := range 3 {
		if err := q.Submit(t.Context(), "chat-a", func(context.Context) {
			probe.enter(i)
			started <- i
			<-release
			probe.leave()
		}); err != nil {
			t.Fatalf("Submit %d: %v", i, err)
		}
	}
	select {
	case got := <-started:
		if got != 0 {
			t.Fatalf("first started job = %d, want message 0", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no job started")
	}
	close(release)
	q.Close()
	order, peak := probe.snapshot()
	if peak != 1 {
		t.Fatalf("one chat ran %d jobs concurrently, want 1", peak)
	}
	if len(order) != 3 || order[0] != 0 || order[1] != 1 || order[2] != 2 {
		t.Fatalf("run order = %v, want 0,1,2", order)
	}
}

func testChatQueueOverlap(t *testing.T) {
	q := gateway.NewChatQueue(2)
	defer q.Close()
	var probe queueProbe
	aStarted := make(chan struct{})
	bStarted := make(chan struct{})
	release := make(chan struct{})
	if err := q.Submit(t.Context(), "chat-a", func(context.Context) {
		probe.enter(0)
		close(aStarted)
		<-release
		probe.leave()
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.Submit(t.Context(), "chat-b", func(context.Context) {
		probe.enter(1)
		close(bStarted)
		<-release
		probe.leave()
	}); err != nil {
		t.Fatal(err)
	}
	for name, ch := range map[string]chan struct{}{"chat-a": aStarted, "chat-b": bStarted} {
		select {
		case <-ch:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s never started while the other chat was running", name)
		}
	}
	if _, peak := probe.snapshot(); peak != 2 {
		t.Fatalf("cross-chat peak = %d, want 2", peak)
	}
	close(release)
}
