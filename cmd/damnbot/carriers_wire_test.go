package main

import (
	"bytes"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/carrierstore"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// carrierDB opens a run database with one assistant event per session.
func carrierDB(t *testing.T, sessions ...string) (*ctxdb.Store, map[string]int64) {
	t.Helper()
	ctx := t.Context()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close ctxdb: %v", cerr)
		}
	})
	events := make(map[string]int64, len(sessions))
	for _, sid := range sessions {
		if _, serr := db.CreateSession(ctx, ctxdb.Session{ID: sid, Origin: "headless", Cwd: "/"}); serr != nil {
			t.Fatal(serr)
		}
		ids, aerr := db.Append(ctx, sid, nil, ctxdb.Event{Kind: "assistant_message", Role: "assistant", Payload: "{}"})
		if aerr != nil {
			t.Fatal(aerr)
		}
		events[sid] = ids[0]
	}
	return db, events
}

func wireCarrier(raw string) event.Carrier {
	return event.Carrier{Family: "anthropic", Model: "claude", Kind: "thinking", Anchor: "0", Raw: []byte(raw), Replayable: true}
}

// TestCarrierHistoryIsolationEndToEnd: the composition root loads carriers
// per session, and the provider request of one session never contains the
// other session's opaque bytes (GAP-PROV-01 cross-provider/session boundary).
func assertHistoryCarrier(t *testing.T, history []loop.Message, want, not string) {
	t.Helper()
	if len(history) != 1 || len(history[0].Carriers) != 1 ||
		!bytes.Contains(history[0].Carriers[0].Raw, []byte(want)) || bytes.Contains(history[0].Carriers[0].Raw, []byte(not)) {
		t.Fatalf("history = %+v, want carrier %s only", history, want)
	}
}

func assertRequestCarrier(t *testing.T, body []byte, want, not string) {
	t.Helper()
	if !bytes.Contains(body, []byte(want)) || bytes.Contains(body, []byte(not)) {
		t.Fatalf("request body wrong carrier: %s", body)
	}
}

func TestCarrierHistoryIsolationEndToEnd(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	db, events := carrierDB(t, "A", "B")
	store := &ctxCarrierStore{db: db}
	if err := store.SaveCarriers(ctx, "A", events["A"], []event.Carrier{wireCarrier(`{"signature":"AAA"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveCarriers(ctx, "B", events["B"], []event.Carrier{wireCarrier(`{"signature":"BBB"}`)}); err != nil {
		t.Fatal(err)
	}
	cs := &ctxStore{db: db}
	historyA, err := cs.History(ctx, "A")
	if err != nil {
		t.Fatal(err)
	}
	historyB, err := cs.History(ctx, "B")
	if err != nil {
		t.Fatal(err)
	}
	assertHistoryCarrier(t, historyA, "AAA", "BBB")
	assertHistoryCarrier(t, historyB, "BBB", "AAA")

	srv := providertest.New(t, nil, providertest.Text("ok"), providertest.Text("ok"))
	net := netx.New(netx.Config{})
	t.Cleanup(net.CloseIdleConnections)
	const ref = "anthropic/claude"
	client, err := provider.NewOpenAI(net.Client("inference", netx.Inference), &provider.Config{
		Routes: []provider.Route{{
			Provider: "anthropic", Model: "claude", Family: "anthropic",
			BaseURL: srv.URL, APIKey: "sk-test",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &providerAdapter{client: client, model: ref}
	for _, c := range []struct {
		history []loop.Message
		want    string
		not     string
	}{{historyA, "AAA", "BBB"}, {historyB, "BBB", "AAA"}} {
		if _, serr := adapter.Stream(ctx, &loop.Request{Model: ref, Messages: c.history}, func(loop.Delta) {}); serr != nil {
			t.Fatal(serr)
		}
		assertRequestCarrier(t, srv.Requests()[len(srv.Requests())-1], c.want, c.not)
	}
}

// TestCarrierRejectBySHA: a signature-invalid retry names carriers by digest,
// and the composition root maps them back to this session's rows only.
func TestCarrierRejectBySHA(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	db, events := carrierDB(t, "A", "B")
	store := &ctxCarrierStore{db: db}
	c := wireCarrier(`{"signature":"CCC"}`)
	c.SHA = carrierstore.PayloadSHA(c.Raw)
	if err := store.SaveCarriers(ctx, "A", events["A"], []event.Carrier{c}); err != nil {
		t.Fatal(err)
	}
	if err := store.RejectCarriers(ctx, "A", []event.Carrier{c}, "invalid_signature"); err != nil {
		t.Fatal(err)
	}
	rejected, err := (carrierstore.Store{DB: db}).Rejected(ctx, "A")
	if err != nil || len(rejected) != 1 {
		t.Fatalf("rejected = %v, %v", rejected, err)
	}
	if err := store.RejectCarriers(ctx, "B", []event.Carrier{c}, "invalid_signature"); err != nil {
		t.Fatalf("cross-session reject by sha: %v", err)
	}
	if other, oerr := (carrierstore.Store{DB: db}).Rejected(ctx, "B"); oerr != nil || len(other) != 0 {
		t.Fatalf("B rejection = %v, %v", other, oerr)
	}
}
