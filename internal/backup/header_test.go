package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/secret"
)

type downKeyring struct{}

func (downKeyring) Get(string, string) (string, error) { return "", errors.New("keychain unavailable") }
func (downKeyring) Set(string, string, string) error   { return errors.New("keychain unavailable") }
func (downKeyring) Delete(string, string) error        { return errors.New("keychain unavailable") }

func testKey(t *testing.T) secret.BackupKey {
	t.Helper()
	s, err := secret.New(secret.Options{ConfigDir: filepath.Join(t.TempDir(), "cfg"), Keyring: downKeyring{}, BackendPresent: func() bool { return false }})
	if err != nil {
		t.Fatal(err)
	}
	k, err := s.Backup(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// openHeaderForTest mirrors the reader half (PR 2b) so the writer half can be
// tested in isolation.
func openHeaderForTest(raw []byte, key secret.BackupKey) (wireHeader, []byte, []byte, []byte, error) {
	h, body, tag, err := parseRawForTest(raw)
	if err != nil {
		return wireHeader{}, nil, nil, nil, err
	}
	dek, prefix, hash, err := unwrapForTest(&h, body, tag, key)
	return h, dek, prefix, hash, err
}

func parseRawForTest(raw []byte) (wireHeader, []byte, []byte, error) {
	var h wireHeader
	if len(raw) < len(magic)+3 || string(raw[:len(magic)]) != magic || raw[len(magic)] != formatVersion {
		return h, nil, nil, ErrIncompatible
	}
	hlen := binary.BigEndian.Uint16(raw[len(magic)+1:])
	start := len(magic) + 3
	if int(hlen) > len(raw)-start-tagSize {
		return h, nil, nil, ErrTruncated
	}
	body := raw[start : start+int(hlen)]
	tag := raw[start+int(hlen) : start+int(hlen)+tagSize]
	if err := json.Unmarshal(body, &h); err != nil {
		return h, nil, nil, ErrCorrupt
	}
	return h, body, tag, nil
}

func unwrapForTest(h *wireHeader, body, tag []byte, key secret.BackupKey) ([]byte, []byte, []byte, error) {
	epub, err := decodeB64(h.Ephemeral, keySize)
	if err != nil {
		return nil, nil, nil, err
	}
	bind := append(append([]byte{}, epub...), key.Public...)
	shared, err := sharedSecret(key.Private, epub)
	if err != nil {
		return nil, nil, nil, err
	}
	hk, err := derive(shared, "dmbk1/header", bind)
	if err != nil {
		return nil, nil, nil, err
	}
	a, err := newAEAD(hk)
	if err != nil {
		return nil, nil, nil, err
	}
	hn, err := decodeB64(h.HeaderNonce, nonceSize)
	if err != nil {
		return nil, nil, nil, err
	}
	if _, oerr := a.Open(nil, hn, tag, headerAAD(uint16(len(body)), body)); oerr != nil { //nolint:gosec // test fixture <= headerMax
		return nil, nil, nil, ErrAuth
	}
	if h.Recipient != key.ID {
		return nil, nil, nil, ErrAuth
	}
	dek, prefix, err := unwrapDEKForTest(h, shared, bind)
	if err != nil {
		return nil, nil, nil, err
	}
	sum := sha256.Sum256(body)
	return dek, prefix, sum[:], nil
}

func unwrapDEKForTest(h *wireHeader, shared, bind []byte) ([]byte, []byte, error) {
	wn, err := decodeB64(h.WrapNonce, nonceSize)
	if err != nil {
		return nil, nil, err
	}
	wrapped, err := base64.StdEncoding.DecodeString(h.WrappedDEK)
	if err != nil {
		return nil, nil, ErrCorrupt
	}
	wk, err := derive(shared, "dmbk1/wrap", bind)
	if err != nil {
		return nil, nil, err
	}
	wa, err := newAEAD(wk)
	if err != nil {
		return nil, nil, err
	}
	dek, err := wa.Open(nil, wn, wrapped, dekAAD(h))
	if err != nil {
		return nil, nil, ErrAuth
	}
	prefix, err := decodeB64(h.NoncePrefix, prefixSize)
	return dek, prefix, err
}

func TestHeaderRoundTrip(t *testing.T) {
	key := testKey(t)
	created := time.Unix(1_700_000_000, 0).UTC()
	var buf bytes.Buffer
	prefix, hash, dek, err := writeHeader(&buf, key, &WriterOptions{
		CreatedAt: created, App: "test", IncludeSecrets: true, ManifestSHA256: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(prefix) != prefixSize || len(hash) != 32 || len(dek) != keySize {
		t.Fatalf("sizes = %d/%d/%d", len(prefix), len(hash), len(dek))
	}
	wh, rdek, rprefix, rhash, err := openHeaderForTest(buf.Bytes(), key)
	if err != nil {
		t.Fatal(err)
	}
	checkHeader(t, &wh, key, created)
	if !bytes.Equal(dek, rdek) || !bytes.Equal(prefix, rprefix) || !bytes.Equal(hash, rhash) {
		t.Fatal("header secrets differ")
	}
}

func checkHeader(t *testing.T, h *wireHeader, key secret.BackupKey, created time.Time) {
	t.Helper()
	if h.V != formatVersion || h.CreatedAt != created.Unix() || h.App != "test" || !h.IncludeSecrets ||
		h.Chunk != chunkSize || h.Recipient != key.ID || h.ManifestSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("header %+v", h)
	}
}

func TestHeaderWrongKey(t *testing.T) {
	key := testKey(t)
	other := testKey(t)
	var buf bytes.Buffer
	if _, _, _, err := writeHeader(&buf, key, &WriterOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := openHeaderForTest(buf.Bytes(), other); !errors.Is(err, ErrAuth) {
		t.Fatalf("wrong key = %v, want ErrAuth", err)
	}
}

func TestHeaderTamper(t *testing.T) {
	key := testKey(t)
	var buf bytes.Buffer
	if _, _, _, err := writeHeader(&buf, key, &WriterOptions{}); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	for i := range raw {
		bad := append([]byte(nil), raw...)
		bad[i] ^= 0x01
		if _, _, _, _, err := openHeaderForTest(bad, key); err == nil {
			t.Fatalf("byte %d: tampered header accepted", i)
		}
	}
	for i := range raw {
		if _, _, _, _, err := openHeaderForTest(raw[:i], key); err == nil {
			t.Fatalf("truncated at %d accepted", i)
		}
	}
}

func TestHeaderIncompatible(t *testing.T) {
	key := testKey(t)
	var buf bytes.Buffer
	if _, _, _, err := writeHeader(&buf, key, &WriterOptions{}); err != nil {
		t.Fatal(err)
	}
	bad := append([]byte(nil), buf.Bytes()...)
	bad[0] = 'X'
	if _, _, _, _, err := openHeaderForTest(bad, key); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("magic = %v", err)
	}
	bad = append([]byte(nil), buf.Bytes()...)
	bad[len(magic)] = 9
	if _, _, _, _, err := openHeaderForTest(bad, key); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("version = %v", err)
	}
}

func TestHeaderManifestSHA(t *testing.T) {
	key := testKey(t)
	var buf bytes.Buffer
	if _, _, _, err := writeHeader(&buf, key, &WriterOptions{ManifestSHA256: "not-hex"}); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("bad manifest = %v", err)
	}
}
