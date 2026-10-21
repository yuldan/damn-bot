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
	raw, rerr := os.ReadFile(DBPath(dir))
	if rerr != nil {
		t.Fatal(rerr)
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

func TestAtRestNoPlaintextCanary(t *testing.T) {
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
	appendMsg(t, s, "S", "canary-events")
	canaries := []string{"canary-events", "canary-summary", "canary-memory", "canary-carrier", "canary-gateway"}
	if err := seedCanaries(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	raw, rerr := os.ReadFile(DBPath(dir))
	if rerr != nil {
		t.Fatal(rerr)
	}
	for _, c := range canaries {
		if bytes.Contains(raw, []byte(c)) {
			t.Fatalf("plaintext canary %q found in encrypted DB", c)
		}
	}
}

func seedCanaries(ctx context.Context, s *Store) error {
	return s.Write(ctx, func(tx *Tx) error {
		var eventID int64
		if xerr := tx.Get(`SELECT max(id) FROM events`, nil, &eventID); xerr != nil {
			return xerr
		}
		ce, stored, serr := tx.SealText("summaries.content", "canary-summary", "{}")
		if serr != nil {
			return serr
		}
		if _, xerr := tx.Exec(`INSERT INTO summaries(session_id,from_seq,to_seq,content,content_enc,model,input_tokens,output_tokens,event_id,created_at)
			VALUES ('S',1,1,?,?,'m',0,0,?,1)`, stored, nullBytesTest(ce), eventID); xerr != nil {
			return xerr
		}
		me, mstored, merr := tx.SealText("memory.content", "canary-memory", "")
		if merr != nil {
			return merr
		}
		if _, xerr := tx.Exec(`INSERT INTO memory(kind,category,scope,name,content,content_enc,content_len,origin,created_at)
			VALUES ('memory','fact','user',NULL,?,?,?, 'user',1)`, mstored, nullBytesTest(me), len("canary-memory")); xerr != nil {
			return xerr
		}
		re, _, rerr2 := tx.SealText("reasoning_carriers.payload", "canary-carrier", "")
		if rerr2 != nil {
			return rerr2
		}
		if _, xerr := tx.Exec(`INSERT INTO reasoning_carriers(event_id,ord,provider_family,model,kind,payload,payload_enc,payload_sha,created_at)
			VALUES (?,0,'p','m','thought_signature',X'',?, 'h',1)`, eventID, nullBytesTest(re)); xerr != nil {
			return xerr
		}
		ge, gstored, gerr := tx.SealText("gateway_inbound.payload", "canary-gateway", "{}")
		if gerr != nil {
			return gerr
		}
		_, xerr := tx.Exec(`INSERT INTO gateway_inbound(platform,external_id,chat_id,sender_id,payload,payload_enc,received_at)
			VALUES ('telegram','1','c','s',?,?,1)`, gstored, nullBytesTest(ge))
		return xerr
	})
}

func nullBytesTest(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func TestAtRestSwappedOtherTables(t *testing.T) { //nolint:gocognit // table swap matrix
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
	appendMsg(t, s, "S", "event")
	var id1, id2 int64
	if err := s.Write(context.Background(), func(tx *Tx) error {
		for i := range 2 {
			enc, _, serr := tx.SealRow("summaries.content", "S|1:1", "sum", "{}")
			if serr != nil {
				return serr
			}
			if _, xerr := tx.Exec(`INSERT INTO summaries(session_id,from_seq,to_seq,content,content_enc,model,input_tokens,output_tokens,event_id,created_at)
				VALUES ('S',1,1,'{}',?,'m',0,0,1,1)`, enc); xerr != nil {
				return xerr
			}
			enc, _, serr = tx.SealRow("memory.content", "user|"+string(rune('a'+i)), "mem", "")
			if serr != nil {
				return serr
			}
			if _, xerr := tx.Exec(`INSERT INTO memory(kind,category,scope,name,content,content_enc,content_len,enc_id,origin,created_at)
				VALUES ('memory','fact','user',NULL,'',?,1,?,'user',1)`, enc, string(rune('a'+i))); xerr != nil {
				return xerr
			}
			if i == 0 {
				id1 = 1
			} else {
				id2 = 2
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Swapping memory ciphertexts between rows must fail identity checks.
	if err := s.tx(context.Background(), modePurge, func(tx *Tx) error {
		_, xerr := tx.Exec(`UPDATE memory SET content_enc = (SELECT content_enc FROM memory WHERE id=1) WHERE id=2`)
		return xerr
	}); err != nil {
		t.Fatal(err)
	}
	var enc2 []byte
	if err := s.r.QueryRowContext(context.Background(), `SELECT content_enc FROM memory WHERE id=2`).Scan(&enc2); err != nil {
		t.Fatal(err)
	}
	if _, err := s.openRow("memory.content", "user|b", enc2, ""); err == nil {
		t.Fatal("swapped memory ciphertext decrypted")
	}
	_ = id1
	_ = id2
}
