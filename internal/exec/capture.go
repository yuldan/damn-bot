package exec

import (
	"bytes"
	"sync"
)

// capture keeps the first and last OutputCap/2 bytes of the output; the
// tail is a fixed ring, so arbitrarily long output costs O(len) copying.
type capture struct {
	mu    sync.Mutex
	head  []byte
	ring  []byte // allocated when head is full
	pos   int    // next write offset in ring
	full  bool   // ring wrapped at least once
	total int64
}

func newCapture() *capture { return &capture{} }

func (k *capture) write(p []byte) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.total += int64(len(p))
	const half = OutputCap / 2
	if room := half - len(k.head); room > 0 {
		n := min(room, len(p))
		k.head = append(k.head, p[:n]...)
		p = p[n:]
	}
	if len(p) == 0 {
		return
	}
	if k.ring == nil {
		k.ring = make([]byte, half)
	}
	if len(p) >= half { // only the last half bytes survive
		p = p[len(p)-half:]
	}
	n := copy(k.ring[k.pos:], p)
	if n < len(p) {
		copy(k.ring, p[n:])
		k.full = true
	}
	k.pos = (k.pos + len(p)) % half
	if k.pos == 0 && len(p) > 0 {
		k.full = true
	}
}

func (k *capture) bytes() ([]byte, bool, int64) {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]byte, 0, len(k.head)+len(k.ring))
	out = append(out, k.head...)
	if k.full {
		out = append(out, k.ring[k.pos:]...)
	}
	out = append(out, k.ring[:k.pos]...)
	return out, k.total > OutputCap, k.total
}

// tail returns a copy of the last n bytes of the output.
func (k *capture) tail(n int) []byte {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.ring == nil {
		return bytes.Clone(k.head[max(len(k.head)-n, 0):])
	}
	out := make([]byte, 0, n)
	if k.full {
		out = append(out, k.ring[k.pos:]...)
	}
	out = append(out, k.ring[:k.pos]...)
	return out[max(len(out)-n, 0):]
}

// size is the number of bytes written so far.
func (k *capture) size() int64 {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.total
}
