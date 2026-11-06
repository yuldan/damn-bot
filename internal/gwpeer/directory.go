package gwpeer

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ChannelDirectory is the cross-platform channel list (GW-023). It stores only
// routing metadata: no token, secret, credential, or message content has a
// field here, so a directory dump cannot leak one.
type ChannelDirectory struct {
	mu      sync.Mutex
	entries map[string]ChannelEntry
}

// ChannelEntry is one routable channel.
type ChannelEntry struct {
	Platform string `json:"platform"`
	Account  string `json:"account"`
	ChatID   string `json:"chat_id"`
	ThreadID string `json:"thread_id,omitempty"`
	Label    string `json:"label,omitempty"`
}

// ErrInvalidChannel rejects an entry without a platform/account/chat.
var ErrInvalidChannel = errors.New("gwpeer: invalid channel entry")

// Put adds or replaces one channel entry. The platform+account+chat+thread
// tuple is the key, so a renamed label never creates a second row.
func (d *ChannelDirectory) Put(e *ChannelEntry) error {
	for _, f := range [...]struct{ name, value string }{
		{"platform", e.Platform}, {"account", e.Account}, {"chat", e.ChatID},
	} {
		if f.value == "" || len(f.value) > 512 || strings.ContainsAny(f.value, "\x00\r\n") {
			return fmt.Errorf("%w: %s is required or malformed", ErrInvalidChannel, f.name)
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.entries == nil {
		d.entries = make(map[string]ChannelEntry)
	}
	d.entries[channelKey(e)] = *e
	return nil
}

// Rows returns one row per requested platform, sorted by platform. A platform
// with no entry yields an empty row rather than a panic or a missing index.
func (d *ChannelDirectory) Rows(platforms []string) []ChannelEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	rows := make([]ChannelEntry, 0, len(platforms))
	for _, platform := range platforms {
		row, ok := d.firstPlatformLocked(platform)
		if !ok {
			row = ChannelEntry{Platform: platform}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Platform < rows[j].Platform })
	return rows
}

// Get returns every channel of one platform, sorted by chat id.
func (d *ChannelDirectory) Get(platform string) []ChannelEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []ChannelEntry
	for _, e := range d.entries {
		if e.Platform == platform {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Account != out[j].Account {
			return out[i].Account < out[j].Account
		}
		return out[i].ChatID < out[j].ChatID
	})
	return out
}

func (d *ChannelDirectory) firstPlatformLocked(platform string) (ChannelEntry, bool) {
	keys := make([]string, 0, 1)
	for k, e := range d.entries {
		if e.Platform == platform {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return ChannelEntry{}, false
	}
	sort.Strings(keys)
	return d.entries[keys[0]], true
}

func channelKey(e *ChannelEntry) string {
	return e.Platform + "\x1f" + e.Account + "\x1f" + e.ChatID + "\x1f" + e.ThreadID
}
