package carrierstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"go.uber.org/goleak"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// fixture opens a database with two sessions, each holding one assistant
// event; the first is the carrier owner.
func fixture(t *testing.T) (db *ctxdb.Store, store Store, eventID, otherEvent int64) {
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
	for _, sid := range []string{"S", "other"} {
		if _, serr := db.CreateSession(ctx, ctxdb.Session{ID: sid, Origin: "headless", Cwd: "/"}); serr != nil {
			t.Fatal(serr)
		}
	}
	ids, err := db.Append(ctx, "S", nil, ctxdb.Event{Kind: "assistant_message", Role: "assistant", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.Append(ctx, "other", nil, ctxdb.Event{Kind: "assistant_message", Role: "assistant", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	return db, Store{DB: db}, ids[0], other[0]
}

func carriers() []Carrier {
	return []Carrier{
		{
			Ord: 0, Family: "gemini", Model: "gemini-3", Kind: KindThoughtSignature, Anchor: "call_1",
			Payload: []byte{0x00, 0x01, 0xff, '"', '\n', 0x7f}, Replayable: true, CreatedAt: 100,
		},
		{
			Ord: 1, Family: "gemini", Model: "gemini-3", Kind: KindRedactedThinking, Anchor: "",
			Payload: []byte(`{"ciphertext":"ab+/=="}`), Replayable: true, CreatedAt: 101,
		},
		{
			Ord: 2, Family: "gemini", Model: "gemini-3", Kind: KindThinking, Anchor: "part/2",
			Payload: []byte("redacted plaintext"), Replayable: false, CreatedAt: 102,
		},
	}
}

// TestCarrierRoundTrip (GAP-PROV-01): wire bytes are stored and returned
// byte-identically, ordered by ord.
func TestCarrierRoundTrip(t *testing.T) {
	t.Parallel()
	_, store, eventID, _ := fixture(t)
	cs := carriers()
	if _, err := store.Save(t.Context(), "S", eventID, cs); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(t.Context(), "S", []int64{eventID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("loaded %d carriers, want 3", len(got))
	}
	for i := range cs {
		if !sameCarrier(&got[i], &cs[i]) || got[i].ID == 0 || got[i].EventID != eventID {
			t.Errorf("carrier %d = %+v, want %+v", i, got[i], cs[i])
		}
		if !bytes.Equal(got[i].Payload, cs[i].Payload) {
			t.Errorf("carrier %d payload = %x, want %x", i, got[i].Payload, cs[i].Payload)
		}
		if got[i].SHA != PayloadSHA(cs[i].Payload) {
			t.Errorf("carrier %d sha = %s", i, got[i].SHA)
		}
	}
}

// sameCarrier compares the persisted field set with the requested carrier.
func sameCarrier(got *Stored, want *Carrier) bool {
	return got.Ord == want.Ord && got.Family == want.Family && got.Model == want.Model &&
		got.Kind == want.Kind && got.Anchor == want.Anchor &&
		got.Replayable == want.Replayable && got.CreatedAt == want.CreatedAt
}

// TestCarrierRefs: the event payload refs are the sha256 digests of the
// payloads and can be built before the event exists.
func TestCarrierRefs(t *testing.T) {
	t.Parallel()
	_, store, eventID, _ := fixture(t)
	cs := carriers()
	refs, err := store.Save(t.Context(), "S", eventID, cs)
	if err != nil {
		t.Fatal(err)
	}
	want := store.Refs(cs)
	if len(refs) != len(want) || refs[0].Ord != 0 || refs[2].SHA != want[2].SHA {
		t.Fatalf("refs = %+v, want %+v", refs, want)
	}
	if refs[0].SHA != PayloadSHA(cs[0].Payload) {
		t.Fatalf("plaintext store sha = %s, want %s", refs[0].SHA, PayloadSHA(cs[0].Payload))
	}
	payload, err := json.Marshal(refs)
	if err != nil || !strings.Contains(string(payload), `"sha":"`) {
		t.Fatalf("refs json = %s, %v", payload, err)
	}
	if PayloadSHA([]byte("abc")) != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Error("PayloadSHA is not sha256 hex")
	}
}

// TestCarrierLoadScoped: carriers of another session's event are never
// returned, and an empty event list is empty.
func TestCarrierLoadScoped(t *testing.T) {
	t.Parallel()
	_, store, eventID, other := fixture(t)
	if _, err := store.Save(t.Context(), "S", eventID, carriers()); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(t.Context(), "S", []int64{other}); err != nil || len(got) != 0 {
		t.Fatalf("other session carriers = %+v, %v", got, err)
	}
	if got, err := store.Load(t.Context(), "S", nil); err != nil || got != nil {
		t.Fatalf("empty load = %+v, %v", got, err)
	}
	if got, err := store.Load(t.Context(), "", []int64{eventID}); !errors.Is(err, ErrCarrier) || got != nil {
		t.Fatalf("empty session = %+v, %v", got, err)
	}
}

// TestCarrierSaveValidation: malformed carriers and cross-session events are
// refused without writing rows.
func TestCarrierSaveValidation(t *testing.T) {
	t.Parallel()
	_, store, eventID, other := fixture(t)
	cases := []struct {
		name   string
		sid    string
		event  int64
		mutate func(*Carrier)
	}{
		{"empty payload", "S", eventID, func(c *Carrier) { c.Payload = nil }},
		{"unknown kind", "S", eventID, func(c *Carrier) { c.Kind = "guess" }},
		{"negative ord", "S", eventID, func(c *Carrier) { c.Ord = -1 }},
		{"empty family", "S", eventID, func(c *Carrier) { c.Family = "" }},
		{"padded model", "S", eventID, func(c *Carrier) { c.Model = " m" }},
		{"zero created_at", "S", eventID, func(c *Carrier) { c.CreatedAt = 0 }},
		{"missing event", "S", 999999, func(*Carrier) {}},
		{"event of another session", "S", other, func(*Carrier) {}},
		{"empty session", "", eventID, func(*Carrier) {}},
	}
	for _, c := range cases {
		cp := carriers()[0]
		c.mutate(&cp)
		if _, err := store.Save(t.Context(), c.sid, c.event, []Carrier{cp}); !errors.Is(err, ErrCarrier) {
			t.Errorf("%s: err = %v, want ErrCarrier", c.name, err)
		}
	}
	if _, err := store.Save(t.Context(), "S", eventID, []Carrier{carriers()[0], carriers()[0]}); !errors.Is(err, ErrCarrier) {
		t.Errorf("duplicate ord: %v", err)
	}
	if refs, err := store.Save(t.Context(), "S", eventID, nil); err != nil || refs != nil {
		t.Errorf("empty save = %+v, %v", refs, err)
	}
	if got, err := store.Load(t.Context(), "S", []int64{eventID}); err != nil || len(got) != 0 {
		t.Fatalf("validation wrote rows: %+v, %v", got, err)
	}
}

// TestCarrierAppendOnly: the persistence layer itself refuses UPDATE/DELETE
// on carriers and rejections (SPEC P9).
func TestCarrierAppendOnly(t *testing.T) {
	t.Parallel()
	db, store, eventID, _ := fixture(t)
	if _, err := store.Save(t.Context(), "S", eventID, carriers()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(t.Context(), "S", []int64{eventID})
	if err != nil {
		t.Fatal(err)
	}
	if rerr := store.Reject(t.Context(), "S", []int64{got[0].ID}, "invalid_signature", 200); rerr != nil {
		t.Fatal(rerr)
	}
	for _, q := range []string{
		"UPDATE reasoning_carriers SET replayable = 0 WHERE id = ?",
		"DELETE FROM reasoning_carriers WHERE id = ?",
		"UPDATE reasoning_carrier_rejections SET error_code = 'x' WHERE carrier_id = ?",
		"DELETE FROM reasoning_carrier_rejections WHERE carrier_id = ?",
	} {
		werr := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
			if _, xerr := tx.Exec(q, got[0].ID); xerr != nil {
				return fmt.Errorf("exec: %w", xerr)
			}
			return nil
		})
		if werr == nil || !strings.Contains(werr.Error(), "append-only") {
			t.Errorf("%q: err = %v, want append-only", q, werr)
		}
	}
}

// TestCarrierRejections: rejection rows are per session, idempotent, and
// scoped to the carrier's owner.
func TestCarrierRejections(t *testing.T) {
	t.Parallel()
	db, store, eventID, _ := fixture(t)
	if _, err := store.Save(t.Context(), "S", eventID, carriers()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(t.Context(), "S", []int64{eventID})
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{got[0].ID, got[1].ID}
	if rerr := store.Reject(t.Context(), "S", ids, "invalid_signature", 300); rerr != nil {
		t.Fatal(rerr)
	}
	if rerr := store.Reject(t.Context(), "S", ids, "invalid_signature", 301); rerr != nil {
		t.Fatalf("idempotent reject: %v", rerr)
	}
	var rows int64
	if werr := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		rowsList, ierr := tx.Ints("SELECT carrier_id FROM reasoning_carrier_rejections WHERE session_id = 'S'")
		if ierr != nil {
			return fmt.Errorf("ints: %w", ierr)
		}
		rows = int64(len(rowsList))
		return nil
	}); werr != nil || rows != 2 {
		t.Fatalf("rejection rows = %d, %v; want 2", rows, werr)
	}
	rejected, rerr := store.Rejected(t.Context(), "S")
	if rerr != nil || !rejected[ids[0]] || !rejected[ids[1]] || len(rejected) != 2 {
		t.Fatalf("rejected = %v, %v", rejected, rerr)
	}
	if rerr := store.Reject(t.Context(), "other", ids, "invalid_signature", 302); !errors.Is(rerr, ErrCarrier) {
		t.Errorf("cross-session reject: %v", rerr)
	}
	if rerr := store.Reject(t.Context(), "S", []int64{999999}, "invalid_signature", 303); !errors.Is(rerr, ErrCarrier) {
		t.Errorf("missing carrier: %v", rerr)
	}
	if rerr := store.Reject(t.Context(), "S", ids, "", 304); !errors.Is(rerr, ErrCarrier) {
		t.Errorf("empty code: %v", rerr)
	}
}

// TestCarrierRejectionsRestart: the marker survives closing and reopening
// the database (the 400 retry logic must not forget it).
func TestCarrierRejectionsRestart(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dir := t.TempDir()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := db.CreateSession(ctx, ctxdb.Session{ID: "S", Origin: "headless", Cwd: "/"}); serr != nil {
		t.Fatal(serr)
	}
	ids, err := db.Append(ctx, "S", nil, ctxdb.Event{Kind: "assistant_message", Role: "assistant", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	store := Store{DB: db}
	if _, serr := store.Save(ctx, "S", ids[0], carriers()); serr != nil {
		t.Fatal(serr)
	}
	got, err := store.Load(ctx, "S", ids)
	if err != nil {
		t.Fatal(err)
	}
	if rerr := store.Reject(ctx, "S", []int64{got[0].ID}, "invalid_signature", 300); rerr != nil {
		t.Fatal(rerr)
	}
	if cerr := db.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	db, err = ctxdb.Open(ctx, &ctxdb.Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close ctxdb: %v", cerr)
		}
	}()
	store = Store{DB: db}
	rejected, err := store.Rejected(ctx, "S")
	if err != nil || !rejected[got[0].ID] || len(rejected) != 1 {
		t.Fatalf("rejections after restart = %v, %v", rejected, err)
	}
	if other, oerr := store.Rejected(ctx, "other"); oerr != nil || len(other) != 0 {
		t.Fatalf("other session rejections = %v, %v", other, oerr)
	}
}

// TestCarrierBySHA: a payload digest maps to this session's carrier IDs only.
func TestCarrierBySHA(t *testing.T) {
	t.Parallel()
	_, store, eventID, _ := fixture(t)
	cs := carriers()
	if _, err := store.Save(t.Context(), "S", eventID, cs); err != nil {
		t.Fatal(err)
	}
	sha := PayloadSHA(cs[1].Payload)
	ids, err := store.BySHA(t.Context(), "S", []string{sha})
	if err != nil || len(ids) != 1 {
		t.Fatalf("BySHA = %v, %v", ids, err)
	}
	got, gerr := store.Load(t.Context(), "S", []int64{eventID})
	if gerr != nil || len(got) != 3 {
		t.Fatal(gerr)
	}
	if got[1].ID != ids[0] || got[1].SHA != sha {
		t.Fatalf("ids = %v, row = %+v", ids, got[1])
	}
	if other, oerr := store.BySHA(t.Context(), "other", []string{sha}); oerr != nil || len(other) != 0 {
		t.Fatalf("other session BySHA = %v, %v", other, oerr)
	}
	if empty, eerr := store.BySHA(t.Context(), "S", nil); eerr != nil || empty != nil {
		t.Fatalf("empty = %v, %v", empty, eerr)
	}
}

// TestCarrierSizeCaps (SEC8-5): one carrier and one message are bounded.
func TestCarrierSizeCaps(t *testing.T) {
	t.Parallel()
	_, store, eventID, _ := fixture(t)
	big := carriers()[0]
	big.Payload = make([]byte, MaxCarrierBytes+1)
	if _, err := store.Save(t.Context(), "S", eventID, []Carrier{big}); !errors.Is(err, ErrCarrier) {
		t.Errorf("oversized carrier: %v", err)
	}
	var many []Carrier
	for i := range 5 {
		c := carriers()[0]
		c.Ord = i
		c.Payload = make([]byte, 230<<10)
		many = append(many, c)
	}
	if _, err := store.Save(t.Context(), "S", eventID, many); !errors.Is(err, ErrCarrier) {
		t.Errorf("oversized message: %v", err)
	}
	if got, err := store.Load(t.Context(), "S", []int64{eventID}); err != nil || len(got) != 0 {
		t.Fatalf("size caps wrote rows: %+v, %v", got, err)
	}
}
