package main

import (
	"context"
	"io"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
)

func TestSendSpoolQueuesOnce(t *testing.T) {
	dir := t.TempDir()
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	g := &serveGateway{dataDir: dir, store: gwstore.Store{DB: db}, stderr: io.Discard}
	req := sendSpoolRequest{Platform: "telegram", Chat: "42", Thread: "7", Text: "hello", Key: "k1"}
	for range 2 {
		if qerr := queueSend(dir, &req); qerr != nil {
			t.Fatal(qerr)
		}
	}
	g.drainSendSpool(context.Background())
	var n int64
	if err := db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT count(*) FROM gateway_deliveries`, nil, &n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}
