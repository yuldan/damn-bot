package gateway

import (
	"errors"
	"sync"
)

// Receipt is what the gateway remembers about one delivered message: the
// platform message id and how many edit-stream updates it already applied.
type Receipt struct {
	MessageID string
	Edits     int
}

// ErrLedgerConflict rejects a second, different platform message id for the
// same (platform, chat, key): that would be a second message, not an edit.
var ErrLedgerConflict = errors.New("gateway: delivery key already has a message")

type ledgerKey struct{ platform, chat, key string }

// Ledger tracks the delivered message of each outbound key so edit-stream
// updates that message instead of sending a new one (SPEC §9.7 edit-stream).
// It is process-local and belongs to the turn that streams; two chats never
// share an entry because the key includes the chat.
type Ledger struct {
	mu   sync.Mutex
	sent map[ledgerKey]Receipt
}

// NewLedger returns an empty ledger.
func NewLedger() *Ledger {
	return &Ledger{sent: make(map[ledgerKey]Receipt)}
}

// RecordSent binds key to the platform message id of its first successful
// send. Re-recording the same id is idempotent; a different id is
// ErrLedgerConflict.
func (l *Ledger) RecordSent(platform, chat, key, messageID string) (Receipt, error) {
	if platform == "" || chat == "" || key == "" || messageID == "" {
		return Receipt{}, ErrInvalidDelivery
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	k := ledgerKey{platform, chat, key}
	if r, ok := l.sent[k]; ok {
		if r.MessageID != messageID {
			return r, ErrLedgerConflict
		}
		return r, nil
	}
	r := Receipt{MessageID: messageID}
	l.sent[k] = r
	return r, nil
}

// RecordEdit counts one edit-stream update of key's existing message. ok is
// false when nothing was sent yet: the caller must send a new message, not
// edit.
func (l *Ledger) RecordEdit(platform, chat, key string) (Receipt, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	k := ledgerKey{platform, chat, key}
	r, ok := l.sent[k]
	if !ok {
		return Receipt{}, false
	}
	r.Edits++
	l.sent[k] = r
	return r, true
}

// Receipt returns the recorded receipt of key.
func (l *Ledger) Receipt(platform, chat, key string) (Receipt, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r, ok := l.sent[ledgerKey{platform, chat, key}]
	return r, ok
}
