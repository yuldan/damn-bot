package buzz

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// event is the NIP-01 wire object of a Nostr relay. Only the fields Damn-Bot
// publishes and reads are modeled.
type event struct {
	ID        string     `json:"id"`
	PubKey    string     `json:"pubkey"`
	CreatedAt int64      `json:"created_at"`
	Kind      int        `json:"kind"`
	Tags      [][]string `json:"tags"`
	Content   string     `json:"content"`
	Sig       string     `json:"sig"`
}

// Event kinds the adapter speaks.
const (
	kindNote    = 1
	kindChannel = 42
)

// canonical is the NIP-01 serialization whose SHA-256 is the event id. HTML
// escaping is disabled so the bytes stay JSON-canonical for the relay.
func canonical(e *event) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	parts := []any{0, e.PubKey, e.CreatedAt, e.Kind, e.Tags, e.Content}
	if err := enc.Encode(parts); err != nil {
		return nil, fmt.Errorf("buzz: canonical event: %w", err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// eventID recomputes the NIP-01 id of e.
func eventID(e *event) (string, error) {
	raw, err := canonical(e)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// signEvent fills ID and Sig for one event. aux is the BIP340 auxiliary
// randomness; production passes fresh entropy, tests pass zeros.
func signEvent(priv [32]byte, kind int, content string, tags [][]string, at time.Time, aux []byte) (event, error) {
	pub := publicKey(priv)
	e := event{
		PubKey:    hex.EncodeToString(pub[:]),
		CreatedAt: at.Unix(),
		Kind:      kind,
		Tags:      tags,
		Content:   content,
	}
	id, err := eventID(&e)
	if err != nil {
		return event{}, err
	}
	e.ID = id
	sig, err := signSchnorr(priv, sha256.Sum256([]byte(id)), aux)
	if err != nil {
		return event{}, fmt.Errorf("buzz: sign event: %w", err)
	}
	e.Sig = hex.EncodeToString(sig[:])
	return e, nil
}

// tagValues returns the values of every tag named key.
func tagValues(tags [][]string, key string) []string {
	var out []string
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == key {
			out = append(out, tag[1])
		}
	}
	return out
}
