package telegram

import (
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/netx"
)

// TestEgressOfflineBlocksGateway: [network] offline refuses the adapter before
// any request reaches the Bot API, even though the fake is loopback.
func TestEgressOfflineBlocksGateway(t *testing.T) {
	f := newFakeTG(t)
	net := netx.New(netx.Config{Offline: true})
	a, err := New(&Config{Token: f.token, BaseURL: f.URL, Doer: EgressClient(net), Store: newMemStore()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Send(t.Context(), &Outbound{ChatID: "7", Text: "x"}); !errors.Is(err, netx.ErrOffline) {
		t.Fatalf("offline send error = %v, want ErrOffline", err)
	}
	if n := f.requests(); n != 0 {
		t.Fatalf("offline adapter made %d requests, want 0", n)
	}
	updates := make(chan Inbound, 1)
	if err := a.Run(t.Context(), updates); !errors.Is(err, netx.ErrOffline) {
		t.Fatalf("offline poll error = %v, want ErrOffline", err)
	}
	if n := f.requests(); n != 0 {
		t.Fatalf("offline poller made %d requests, want 0", n)
	}
}

// TestEgressBackgroundOffStillDelivers: network.background=false is about
// traffic the user did not ask for; a gateway send still goes out.
func TestEgressBackgroundOffStillDelivers(t *testing.T) {
	f := newFakeTG(t)
	net := netx.New(netx.Config{NoBackground: true})
	a, err := New(&Config{Token: f.token, BaseURL: f.URL, Doer: EgressClient(net), Store: newMemStore()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Send(t.Context(), &Outbound{ChatID: "7", Text: "x"}); err != nil {
		t.Fatalf("background=false blocked a user-initiated send: %v", err)
	}
	if n := len(f.callsFor("sendMessage")); n != 1 {
		t.Fatalf("sendMessage calls = %d, want 1", n)
	}
}
