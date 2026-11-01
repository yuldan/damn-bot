package catalog

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
)

func signedManifest(t *testing.T, priv ed25519.PrivateKey, m *Manifest) []byte {
	t.Helper()
	raw, err := m.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, raw))
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func testManifest(kind Kind, serial int64) Manifest {
	return Manifest{
		Kind: kind, Name: "demo", Version: "1.0", Serial: serial,
		Entries: []Entry{{
			ID: "entry", Commit: "0123456789abcdef0123456789abcdef01234567",
			SHA256:  "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			Archive: "https://example.test/entry.tar.gz",
		}},
	}
}

func TestGAPF5_SEC20VerifyPositive(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []Kind{KindProvider, KindMCP} {
		m := testManifest(kind, 7)
		raw := signedManifest(t, priv, &m)
		got, err := Verify(raw, pub, 6)
		if err != nil || got.Kind != kind || got.Serial != 7 || len(got.Entries) != 1 {
			t.Fatalf("%s: %+v, %v", kind, got, err)
		}
		if _, err := VerifyWithStore(raw, pub, &memSeq{last: 6}); err != nil {
			t.Fatalf("%s store: %v", kind, err)
		}
	}
}

func TestGAPF5_SEC20RejectsUnsignedAndBadKey(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m := testManifest(KindMCP, 1)
	raw, merr := json.Marshal(m)
	if merr != nil {
		t.Fatal(merr)
	}
	if _, verr := Verify(raw, pub, 0); !errors.Is(verr, ErrUnsigned) {
		t.Fatalf("unsigned = %v", verr)
	}
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signed := signedManifest(t, priv, &m)
	wrongKey := ed25519.PublicKey(other[32:])
	got, verr := Verify(signed, wrongKey, 0)
	_ = got
	if !errors.Is(verr, ErrSignature) {
		t.Fatalf("wrong key = %v", verr)
	}
	got, verr = Verify(signed, pub[:16], 0)
	_ = got
	if !errors.Is(verr, ErrKey) {
		t.Fatalf("short key = %v", verr)
	}
}

func TestGAPF5_SEC20RejectsTamperAndRollback(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m := testManifest(KindProvider, 9)
	raw := signedManifest(t, priv, &m)
	var tamperedManifest Manifest
	if uerr := json.Unmarshal(raw, &tamperedManifest); uerr != nil {
		t.Fatal(uerr)
	}
	tamperedManifest.Entries[0].Commit = "ffffffffffffffffffffffffffffffffffffffff"
	tampered, err := json.Marshal(tamperedManifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(tampered, pub, 0); !errors.Is(err, ErrSignature) {
		t.Fatalf("tampered = %v", err)
	}
	if _, err := Verify(raw, pub, 9); !errors.Is(err, ErrRollback) {
		t.Fatalf("rollback = %v", err)
	}
}

func TestGAPF5_SEC20RejectsInvalidEntry(t *testing.T) {
	t.Parallel()
	m := testManifest(KindMCP, 1)
	m.Entries[0].SHA256 = "not-a-digest"
	if _, err := ParseManifest(mustJSON(t, m)); !errors.Is(err, ErrEntry) {
		t.Fatalf("entry = %v", err)
	}
	m = testManifest(KindMCP, 0)
	if _, err := ParseManifest(mustJSON(t, m)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("serial = %v", err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type memSeq struct{ last int64 }

func (m *memSeq) Last(Kind, string) (int64, error) { return m.last, nil }

func (m *memSeq) Save(_ Kind, _ string, serial int64) error {
	m.last = serial
	return nil
}
