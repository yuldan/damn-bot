package ctxdb

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testCodec struct{ id string }

func (c testCodec) ID() string { return c.id }
func (c testCodec) Seal(scope string, p []byte) ([]byte, error) {
	out := append([]byte(scope+"|"), p...)
	for i := len(scope) + 1; i < len(out); i++ {
		out[i] ^= 0x5a
	}
	return out, nil
}

func (c testCodec) Hash(s string) string     { return "h:" + s }
func (c testCodec) FTS(tokens string) string { return strings.ToUpper(tokens) }
func (c testCodec) Open(scope string, b []byte) ([]byte, error) {
	pfx := scope + "|"
	if !bytes.HasPrefix(b, []byte(pfx)) {
		return nil, errors.New("test codec: bad prefix")
	}
	out := append([]byte(nil), b[len(pfx):]...)
	for i := range out {
		out[i] ^= 0x5a
	}
	return out, nil
}

func TestAtRestActivation(t *testing.T) {
	dir := t.TempDir()
	codec := testCodec{id: "dek1"}
	s, err := Open(context.Background(), &Options{Dir: dir, Kind: "cli", Codec: codec})
	if err != nil {
		t.Fatal(err)
	}
	if cerr := s.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if _, oerr := Open(context.Background(), &Options{Dir: dir, Kind: "cli"}); !errors.Is(oerr, ErrEncrypted) {
		t.Fatalf("plaintext open of encrypted DB = %v", oerr)
	}
	s, err = Open(context.Background(), &Options{Dir: dir, Kind: "cli", Codec: codec})
	if err != nil {
		t.Fatal(err)
	}
	session(t, s, "S", "")
	appendMsg(t, s, "S", "secret")
	raw, err := os.ReadFile(DBPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("secret")) {
		t.Fatal("plaintext event content found in DB file")
	}
	evs, err := s.History(context.Background(), "S", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].Content != "secret" || evs[0].Payload != "{}" {
		t.Fatalf("history = %+v", evs)
	}
	if cerr := s.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if _, err := Open(context.Background(), &Options{Dir: dir, Kind: "cli", Codec: testCodec{id: "other"}}); !errors.Is(err, ErrEncrypted) {
		t.Fatalf("wrong DEK id = %v", err)
	}
}

func TestAtRestExistingPlaintextRefused(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(context.Background(), &Options{Dir: dir, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	session(t, s, "S", "")
	appendMsg(t, s, "S", "old")
	if cerr := s.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if _, err := Open(context.Background(), &Options{Dir: dir, Kind: "cli", Codec: testCodec{id: "dek"}}); !errors.Is(err, ErrAtRestPlaintext) {
		t.Fatalf("existing plaintext = %v", err)
	}
}

func TestAtRestBlob(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(context.Background(), &Options{Dir: dir, Kind: "cli", Codec: testCodec{id: "dek"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := s.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	p, err := s.PutBlob([]byte("blob-secret"), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	if werr := s.Write(context.Background(), func(tx *Tx) error { return tx.Publish(p) }); werr != nil {
		t.Fatal(werr)
	}
	stored, err := os.ReadFile(filepath.Join(dir, "blobs", p.Sha[:2], p.Sha)) //nolint:gosec // test data dir
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("blob-secret")) || !strings.HasPrefix(string(stored), "blob:") {
		t.Fatalf("stored blob = %q", stored)
	}
	got, err := s.ReadBlob(context.Background(), p.Sha)
	if err != nil || string(got) != "blob-secret" {
		t.Fatalf("ReadBlob = %q, %v", got, err)
	}
}

func TestAtRestSwappedCiphertextRejected(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(context.Background(), &Options{Dir: dir, Kind: "cli", Codec: testCodec{id: "dek"}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := s.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	session(t, s, "S", "")
	appendMsg(t, s, "S", "first")
	appendMsg(t, s, "S", "second")
	if err := s.tx(context.Background(), modePurge, func(tx *Tx) error {
		_, xerr := tx.Exec(`UPDATE events SET content_enc =
			(SELECT content_enc FROM events WHERE session_id='S' AND seq=1) WHERE session_id='S' AND seq=2`)
		return xerr
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.History(context.Background(), "S", 0); err == nil {
		t.Fatal("swapped ciphertext decrypted")
	}
}

func TestAtRestHashKeyed(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(context.Background(), &Options{Dir: dir, Kind: "cli", Codec: testCodec{id: "dek"}})
	if err != nil {
		t.Fatal(err)
	}
	session(t, s, "S", "")
	appendMsg(t, s, "S", "offline-guess")
	var stored string
	if err := s.r.QueryRowContext(context.Background(), `SELECT hash FROM events WHERE session_id='S' AND seq=1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	plain := EventHash("", "S", 1, "user_message", "offline-guess", "{}", "")
	if stored == plain {
		t.Fatal("encrypted DB stores the plaintext offline-guessable hash")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}
