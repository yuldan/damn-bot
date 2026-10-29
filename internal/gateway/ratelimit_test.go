package gateway_test

import (
	"errors"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
)

func TestLimiterPerChatAndGlobal(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	l, err := gateway.NewLimiter(gateway.Rate{PerSecond: 1, Burst: 2}, gateway.Rate{PerSecond: 10, Burst: 10})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := l.Take("c1", t0); !ok {
		t.Fatal("first message blocked")
	}
	if ok, _ := l.Take("c1", t0); !ok {
		t.Fatal("burst message blocked")
	}
	ok, wait := l.Take("c1", t0)
	if ok || wait < time.Second {
		t.Fatalf("third message: ok=%v wait=%v, want ~1s", ok, wait)
	}
	if ok, _ := l.Take("c2", t0); !ok {
		t.Fatal("another chat was blocked by c1")
	}
	if ok, _ := l.Take("c1", t0.Add(wait)); !ok {
		t.Fatal("chat not allowed after the reported wait")
	}

	g, err := gateway.NewLimiter(gateway.Rate{PerSecond: 1, Burst: 10}, gateway.Rate{PerSecond: 1, Burst: 1})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := g.Take("c1", t0); !ok {
		t.Fatal("global token unavailable")
	}
	if ok, wait := g.Take("c2", t0); ok || wait < time.Second {
		t.Fatalf("global bucket: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := g.Take("c2", t0.Add(time.Second)); !ok {
		t.Fatal("global token not refilled")
	}
}

// TestLimiterRetryAfter: a platform retry_after blocks only that chat, and the
// reported wait is exactly the deadline.
func TestLimiterRetryAfter(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	l, err := gateway.NewLimiter(gateway.Rate{PerSecond: 1, Burst: 10}, gateway.Rate{PerSecond: 10, Burst: 10})
	if err != nil {
		t.Fatal(err)
	}
	l.Penalize("c1", t0.Add(30*time.Second))
	if ok, wait := l.Take("c1", t0); ok || wait != 30*time.Second {
		t.Fatalf("penalized chat: ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.Take("c2", t0); !ok {
		t.Fatal("penalty leaked to another chat")
	}
	if ok, _ := l.Take("c1", t0.Add(30*time.Second)); !ok {
		t.Fatal("penalty did not expire")
	}
}

// TestLimitDoesNotDropMessages: pacing delays sends but every message is
// eventually delivered exactly once (the turn is never blocked).
func TestLimitDoesNotDropMessages(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	l, err := gateway.NewLimiter(gateway.Rate{PerSecond: 1, Burst: 1}, gateway.Rate{PerSecond: 5, Burst: 5})
	if err != nil {
		t.Fatal(err)
	}
	queues := map[string][]string{"c1": {"a", "b", "c", "d", "e"}, "c2": {"f", "g", "h", "i", "j"}}
	delivered := map[string]int{}
	now := t0
	for deadline := t0.Add(30 * time.Second); now.Before(deadline) && len(delivered) < 10; now = now.Add(250 * time.Millisecond) {
		for _, chat := range []string{"c1", "c2"} {
			q := queues[chat]
			if len(q) == 0 {
				continue
			}
			if ok, _ := l.Take(chat, now); ok {
				delivered[q[0]]++
				queues[chat] = q[1:]
			}
		}
	}
	if len(delivered) != 10 {
		t.Fatalf("delivered %d/10 messages, queues=%v", len(delivered), queues)
	}
	for k, n := range delivered {
		if n != 1 {
			t.Errorf("message %q delivered %d times", k, n)
		}
	}
}

func TestLimiterValidation(t *testing.T) {
	for _, c := range []struct{ perChat, global gateway.Rate }{
		{gateway.Rate{PerSecond: 0, Burst: 1}, gateway.Rate{PerSecond: 1, Burst: 1}},
		{gateway.Rate{PerSecond: 1, Burst: 0}, gateway.Rate{PerSecond: 1, Burst: 1}},
		{gateway.Rate{PerSecond: 1, Burst: 1}, gateway.Rate{PerSecond: 0, Burst: 1}},
		{gateway.Rate{PerSecond: 1, Burst: 1}, gateway.Rate{PerSecond: 1, Burst: 0}},
	} {
		if _, err := gateway.NewLimiter(c.perChat, c.global); !errors.Is(err, gateway.ErrInvalidRate) {
			t.Errorf("NewLimiter(%+v, %+v) = %v", c.perChat, c.global, err)
		}
	}
}
