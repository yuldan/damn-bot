package e2e

// The Telegram adapter against the shared fake Bot API. These tests exercise
// the real netx egress path (EgressClient) with the adapter: polling, a
// threaded reply, and the [network] negative controls that the fake can count
// exactly (offline = 0 requests, background=false still delivers).

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway/telegram"
	"github.com/damnbot/damnbot/internal/netx"
)

// tgAdapterStore is the adapter's durable state for one test.
type tgAdapterStore struct {
	mu      sync.Mutex
	offset  int64
	ids     map[string]int64
	claimed map[string]bool
}

func newTGAdapterStore() *tgAdapterStore {
	return &tgAdapterStore{ids: map[string]int64{}, claimed: map[string]bool{}}
}

func (s *tgAdapterStore) Offset(context.Context, string) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.offset, nil
}

func (s *tgAdapterStore) PutOffset(_ context.Context, _ string, next int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offset = next
	return nil
}

func (s *tgAdapterStore) MessageID(_ context.Context, key string) (int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.ids[key]
	return id, ok, nil
}

func (s *tgAdapterStore) PutMessageID(_ context.Context, key string, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ids[key] = id
	return nil
}

func (s *tgAdapterStore) ClaimCallback(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed[id] {
		return false, nil
	}
	s.claimed[id] = true
	return true, nil
}

// newTelegramAdapter wires the adapter to the fake through netx.
func newTelegramAdapter(t *testing.T, s *FakeTG, cfg netx.Config) *telegram.Adapter {
	t.Helper()
	a, err := telegram.New(&telegram.Config{
		Token:   FakeTGToken,
		BaseURL: s.URL,
		Doer:    telegram.EgressClient(netx.New(cfg)),
		Store:   newTGAdapterStore(),
	})
	if err != nil {
		t.Fatalf("telegram.New: %v", err)
	}
	return a
}

func TestTelegramAdapterPollsAndRepliesInTopic(t *testing.T) {
	s := NewFakeTG(t, FakeTGOptions{})
	a := newTelegramAdapter(t, s, netx.Config{})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	in := make(chan telegram.Inbound, 4)
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, in) }()

	s.PushText(1001, "halo")
	var ib telegram.Inbound
	select {
	case ib = <-in:
	case <-time.After(3 * time.Second):
		t.Fatal("poller did not deliver the update")
	}
	if ib.UpdateID != "u:1" || ib.ChatID != "1001" || ib.SenderID != "1001" || ib.Text != "halo" {
		t.Fatalf("inbound = %+v", ib)
	}

	id, err := a.Send(ctx, telegram.Outbound{ChatID: ib.ChatID, ThreadID: "7", Text: "balas", Key: "k1"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if id == 0 {
		t.Fatal("send returned no message id")
	}
	sends := s.RequestsFor("sendMessage")
	if len(sends) != 1 || sends[0].Params["message_thread_id"] != "7" {
		t.Fatalf("sends = %+v, want one in thread 7", sends)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := s.RequestsFor("getUpdates"); len(got) == 0 || got[0].Offset != 0 {
		t.Errorf("polls = %+v, want the stored offset 0 on the first call", got)
	}
}

func TestTelegramAdapterOfflineSendsNothing(t *testing.T) {
	s := NewFakeTG(t, FakeTGOptions{})
	a := newTelegramAdapter(t, s, netx.Config{Offline: true})
	_, err := a.Send(t.Context(), telegram.Outbound{ChatID: "1001", Text: "x"})
	if !errors.Is(err, netx.ErrOffline) {
		t.Fatalf("offline send error = %v, want ErrOffline", err)
	}
	if n := len(s.Requests()); n != 0 {
		t.Fatalf("offline adapter sent %d requests, want 0", n)
	}
}

func TestTelegramAdapterBackgroundOffStillSends(t *testing.T) {
	s := NewFakeTG(t, FakeTGOptions{})
	a := newTelegramAdapter(t, s, netx.Config{NoBackground: true})
	id, err := a.Send(t.Context(), telegram.Outbound{ChatID: "1001", Text: "x"})
	if err != nil {
		t.Fatalf("background=false blocked a user-initiated send: %v", err)
	}
	if id == 0 {
		t.Fatal("no message id")
	}
	if n := len(s.RequestsFor("sendMessage")); n != 1 {
		t.Fatalf("sendMessage requests = %d, want 1", n)
	}
	if n := len(s.RequestsFor("getUpdates")); n != 0 {
		t.Errorf("idle adapter polled %d times, want 0", n)
	}
}
