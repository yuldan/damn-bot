package gwpeer

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestPeerBookRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := dir + "/peer/peers.json"
	book, err := OpenBook(path)
	if err != nil {
		t.Fatal(err)
	}
	p := Peer{ID: "b", URL: "https://peer.example", TokenRef: "B_TOKEN", PublicKey: pub(t, testKey(t))}
	if aerr := book.Add(&p, time.Now()); aerr != nil {
		t.Fatal(aerr)
	}
	if aerr := book.Add(&p, time.Now()); !errors.Is(aerr, ErrPeerExists) {
		t.Fatalf("duplicate add = %v, want ErrPeerExists", aerr)
	}
	if cerr := book.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	reopened, err := OpenBook(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := reopened.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()
	got, ok := reopened.Get("b")
	if !ok || got.URL != p.URL || !bytes.Equal(got.PublicKey, p.PublicKey) {
		t.Fatalf("reloaded peer = %+v, ok=%v", got, ok)
	}
	if rerr := reopened.Remove("b"); rerr != nil {
		t.Fatal(rerr)
	}
	if _, ok := reopened.Get("b"); ok {
		t.Fatal("removed peer is still present")
	}
}
