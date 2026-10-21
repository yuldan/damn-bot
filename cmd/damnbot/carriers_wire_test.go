package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
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

// carrierReply is a 2xx stream with an opaque thinking signature.
func carrierReply(signature, thinking string) providertest.Reply {
	reasoning := fmt.Sprintf(`[{"type":"thinking","thinking":%q,"signature":%q}]`, thinking, signature)
	return carrierReplyValue(reasoning)
}

func carrierReplyValue(reasoning string) providertest.Reply {
	return providertest.Reply{Frames: []string{
		fmt.Sprintf(`{"choices":[{"index":0,"delta":{"reasoning":%s,"content":"ok"}}]}`, reasoning),
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5}}`,
		"[DONE]",
	}}
}

// TestCarrierRunPersistsWithRealStore (SEC8-1): a real run with a
// carrier-bearing reply must append the assistant event (payload validator
// accepts carriers:[{ord,sha}]) and persist the carrier in ctxdb.
func TestCarrierRunPersistsWithRealStore(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, carrierReply("SIG-1", "plan"))
	dir := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfg, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := run([]string{
		"run", "-p", "hi", "--model", "openai/gpt-fake", "--base-url", srv.URL,
		"--data-dir", dir, "--config", cfg, "--output", "json",
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d; stdout %s; stderr %s", code, out.String(), errb.String())
	}
	route := srv.URL
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close ctxdb: %v", cerr)
		}
	}()
	var family, model, sha string
	var payload []byte
	if qerr := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get("SELECT provider_family, model, payload_sha, payload FROM reasoning_carriers", nil, &family, &model, &sha, &payload)
	}); qerr != nil {
		t.Fatal(qerr)
	}
	if family != route || model != "gpt-fake" || !bytes.Contains(payload, []byte("SIG-1")) {
		t.Fatalf("carrier = %s/%s %q family %q", family, model, payload, family)
	}
	var eventPayload string
	if qerr := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get("SELECT payload FROM events WHERE kind = 'assistant_message' LIMIT 1", nil, &eventPayload)
	}); qerr != nil {
		t.Fatal(qerr)
	}
	if !bytes.Contains([]byte(eventPayload), []byte(`"carriers"`)) || !bytes.Contains([]byte(eventPayload), []byte(sha)) {
		t.Fatalf("assistant payload = %s, want carriers ref %s", eventPayload, sha)
	}
}

// TestCarrierRedactionDropsBytes (SEC8-3): a secret in thinking text makes
// the carriers non-replayable and they are not stored at all.
func TestCarrierRedactionDropsBytes(t *testing.T) {
	t.Parallel()
	const secret = "sk-abcdefghijklmnopqrstuvwxyz012345"
	srv := providertest.New(t, nil, carrierReply("SIG-2", "the key is "+secret))
	dir := t.TempDir()
	cfg := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(cfg, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errb bytes.Buffer
	code := run([]string{
		"run", "-p", "hi", "--model", "openai/gpt-fake", "--base-url", srv.URL,
		"--data-dir", dir, "--config", cfg, "--output", "json",
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d; stdout %s; stderr %s", code, out.String(), errb.String())
	}
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close ctxdb: %v", cerr)
		}
	}()
	var rows, leaks int64
	if qerr := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		n, ierr := tx.Ints("SELECT id FROM reasoning_carriers")
		if ierr != nil {
			return fmt.Errorf("ints: %w", ierr)
		}
		rows = int64(len(n))
		leaked, lerr := tx.Ints("SELECT id FROM reasoning_carriers WHERE instr(CAST(payload AS TEXT), ?) > 0", secret)
		if lerr != nil {
			return fmt.Errorf("leak query: %w", lerr)
		}
		leaks = int64(len(leaked))
		return nil
	}); qerr != nil {
		t.Fatal(qerr)
	}
	if rows != 0 || leaks != 0 {
		t.Fatalf("carrier rows = %d, secret rows = %d; want none", rows, leaks)
	}
	var sid string
	if qerr := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get("SELECT id FROM sessions LIMIT 1", nil, &sid)
	}); qerr != nil {
		t.Fatal(qerr)
	}
	history, err := (&ctxStore{db: db}).History(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	for i := range history {
		if len(history[i].Carriers) != 0 {
			t.Fatalf("redacted carrier reloaded: %+v", history[i].Carriers)
		}
	}
	// The raw secret must not remain in the database file or its WAL either.
	if derr := filepath.Walk(dir, func(path string, info os.FileInfo, werr error) error {
		if werr != nil || info.IsDir() {
			return werr
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		if bytes.Contains(b, []byte(secret)) {
			t.Errorf("secret bytes remain in %s", path)
		}
		return nil
	}); derr != nil {
		t.Fatal(derr)
	}
}

// TestCarrierRejectedNotReplayed (SEC8-2): after a rejection, history loads
// no carrier and the next request has none of its bytes.
func TestCarrierRejectedNotReplayed(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	db, events := carrierDB(t, "A")
	store := &ctxCarrierStore{db: db}
	c := wireCarrier(`{"signature":"REJECTED"}`)
	c.SHA = carrierstore.PayloadSHA(c.Raw)
	if err := store.SaveCarriers(ctx, "A", events["A"], []event.Carrier{c}); err != nil {
		t.Fatal(err)
	}
	if err := store.RejectCarriers(ctx, "A", []event.Carrier{c}, "invalid_signature"); err != nil {
		t.Fatal(err)
	}
	history, err := (&ctxStore{db: db}).History(ctx, "A")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || len(history[0].Carriers) != 0 {
		t.Fatalf("history after rejection = %+v", history)
	}
	srv := providertest.New(t, nil, providertest.Text("ok"))
	net := netx.New(netx.Config{})
	t.Cleanup(net.CloseIdleConnections)
	const ref = "anthropic/claude"
	client, err := provider.NewOpenAI(net.Client("inference", netx.Inference), &provider.Config{
		Routes: []provider.Route{{Provider: "anthropic", Model: "claude", Family: "anthropic", BaseURL: srv.URL, APIKey: "sk-test"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := (&providerAdapter{client: client, model: ref}).Stream(ctx, &loop.Request{Model: ref, Messages: history}, func(loop.Delta) {}); serr != nil {
		t.Fatal(serr)
	}
	if body := srv.Requests()[0]; bytes.Contains(body, []byte("REJECTED")) {
		t.Fatalf("rejected carrier replayed: %s", body)
	}
}
