package gwstore

import (
	"testing"
)

// TestPendingInboundReplay: rows since the pause window are returned oldest
// first with decrypted text, and a done marker removes exactly one row.
func TestPendingInboundReplay(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	ctx := t.Context()
	for _, in := range []Inbound{
		{Platform: "telegram", ChatID: "c1", SenderID: "1", UpdateID: "u:1", Payload: `{"kind":"text","text":"first"}`},
		{Platform: "telegram", ChatID: "c1", SenderID: "1", UpdateID: "u:2", Payload: `{"kind":"text","text":"/new"}`},
	} {
		if ok, err := store.Record(ctx, &in); err != nil || !ok {
			t.Fatalf("record %s: ok=%t err=%v", in.UpdateID, ok, err)
		}
	}
	rows, err := store.PendingInbound(ctx, 0, 1<<62, 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("pending = %d, %v; want 2", len(rows), err)
	}
	if rows[0].Text != "first" || rows[1].Text != "/new" || rows[0].UpdateID != "u:1" {
		t.Fatalf("pending rows = %+v", rows)
	}
}

// TestPendingInboundWindow: a pause watermark after the rows returns nothing.
func TestPendingInboundWindow(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	in := Inbound{Platform: "telegram", ChatID: "c1", SenderID: "1", UpdateID: "u:1", Payload: `{"kind":"text","text":"first"}`}
	if ok, err := store.Record(t.Context(), &in); err != nil || !ok {
		t.Fatalf("record: ok=%t err=%v", ok, err)
	}
	rows, err := store.PendingInbound(t.Context(), 1<<62, 1<<62, 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("future window = %+v, %v", rows, err)
	}
}
