package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/browser/cdp"
)

// Console and image-read bounds (TOOL-008/TOOL-009). The console buffer is a
// bounded ring: page JavaScript can log without limit, so the module keeps the
// newest entries only and never grows with the page.
const (
	// maxConsoleLines is the number of console entries the buffer keeps.
	maxConsoleLines = 200
	// maxConsoleLine caps one entry in runes.
	maxConsoleLine = 2048
	// maxConsoleBytes caps the whole buffer in bytes.
	maxConsoleBytes = 64 << 10
	// maxEventParams refuses an oversized unsolicited event instead of
	// decoding it.
	maxEventParams = 64 << 10
	// maxDrainEvents bounds one drain pass.
	maxDrainEvents = 1024
	// maxImages caps one browser_get_images result.
	maxImages = 100
	// maxImageSrc caps one image URL in runes.
	maxImageSrc = 2048
	// maxImageAlt caps one image alt text in runes.
	maxImageAlt = 512
)

// consoleEntry is one normalized console line: level plus already-clipped text.
type consoleEntry struct{ level, text string }

// parityState is the per-page mutable state of the parity reads and the native
// dialog policy. It is separate from Config so NewPage stays cheap and the
// CDP page, an engine page and a test fake all share one implementation.
type parityState struct {
	mu      sync.Mutex
	logs    []consoleEntry
	logSize int
	logsOn  bool
}

func newParityState() *parityState { return &parityState{} }

// Console returns the buffered console log with the Runtime and Log domains
// enabled, capped by maxConsoleLines/maxConsoleLine/maxConsoleBytes. It never
// evaluates page JavaScript: the buffer is filled from unsolicited protocol
// events only (TOOL-008).
func (p *Page) Console(ctx context.Context) (string, error) {
	if p.conn == nil {
		return "", fmt.Errorf("%w: console", errUnsupported)
	}
	if err := p.enableConsole(ctx); err != nil {
		return "", err
	}
	p.drain()
	return p.consoleText(), nil
}

// enableConsole turns the two log domains on once. A second call is a no-op so
// the buffered entries are not duplicated.
func (p *Page) enableConsole(ctx context.Context) error {
	p.parity.mu.Lock()
	on := p.parity.logsOn
	p.parity.mu.Unlock()
	if on {
		return nil
	}
	for _, method := range []string{"Runtime.enable", "Log.enable"} {
		if _, err := p.conn.Call(ctx, method, nil); err != nil {
			return fmt.Errorf("browser: console: %w", err)
		}
	}
	p.parity.mu.Lock()
	p.parity.logsOn = true
	p.parity.mu.Unlock()
	return nil
}

// drain consumes every already-queued event without blocking. Load events are
// discarded: a console read never owns a navigation, and the next WaitLoad
// arms the page by navigating first.
func (p *Page) drain() {
	for range maxDrainEvents {
		select {
		case ev, ok := <-p.conn.Events():
			if !ok {
				return
			}
			p.ingest(ev)
		default:
			return
		}
	}
}

// ingest records one unsolicited event. Only the three console sources are
// mapped; every other method is ignored, so an unrelated event can never
// change the buffer.
func (p *Page) ingest(ev cdp.Event) {
	if len(ev.Params) > maxEventParams {
		return
	}
	switch ev.Method {
	case "Runtime.consoleAPICalled":
		var msg struct {
			Type string `json:"type"`
			Args []struct {
				Type        string          `json:"type"`
				Value       json.RawMessage `json:"value"`
				Description string          `json:"description"`
			} `json:"args"`
		}
		if err := json.Unmarshal(ev.Params, &msg); err != nil {
			return
		}
		parts := make([]string, 0, len(msg.Args))
		for _, arg := range msg.Args {
			parts = append(parts, consoleArg(arg.Value, arg.Description))
		}
		p.appendLog(msg.Type, strings.Join(parts, " "))
	case "Runtime.exceptionThrown":
		var msg struct {
			Details struct {
				Text      string `json:"text"`
				Exception struct {
					Description string `json:"description"`
				} `json:"exception"`
			} `json:"exceptionDetails"`
		}
		if err := json.Unmarshal(ev.Params, &msg); err != nil {
			return
		}
		text := msg.Details.Text
		if d := msg.Details.Exception.Description; d != "" {
			text = d
		}
		p.appendLog("exception", text)
	case "Log.entryAdded":
		var msg struct {
			Entry struct {
				Level string `json:"level"`
				Text  string `json:"text"`
			} `json:"entry"`
		}
		if err := json.Unmarshal(ev.Params, &msg); err != nil {
			return
		}
		p.appendLog(msg.Entry.Level, msg.Entry.Text)
	}
}

// consoleArg renders one console argument: the JSON value when the protocol
// sent one, otherwise the description of an object or a function.
func consoleArg(value json.RawMessage, description string) string {
	if len(value) > 0 {
		var compact bytes.Buffer
		if err := json.Compact(&compact, value); err == nil {
			return compact.String()
		}
	}
	return description
}

// appendLog appends one entry and trims the oldest until both bounds hold.
func (p *Page) appendLog(level, text string) {
	if level == "" {
		level = "log"
	}
	entry := consoleEntry{level: clip(level, 32), text: clip(text, maxConsoleLine)}
	size := len(entry.text)
	p.parity.mu.Lock()
	defer p.parity.mu.Unlock()
	p.parity.logs = append(p.parity.logs, entry)
	p.parity.logSize += size
	for len(p.parity.logs) > maxConsoleLines || p.parity.logSize > maxConsoleBytes {
		drop := p.parity.logs[0]
		p.parity.logs = p.parity.logs[1:]
		p.parity.logSize -= len(drop.text)
	}
}

// consoleText renders the buffer. Entries arrive in protocol order.
func (p *Page) consoleText() string {
	p.parity.mu.Lock()
	entries := append([]consoleEntry(nil), p.parity.logs...)
	p.parity.mu.Unlock()
	if len(entries) == 0 {
		return "no console messages"
	}
	var out strings.Builder
	for i, entry := range entries {
		if i > 0 {
			out.WriteByte('\n')
		}
		out.WriteString("[" + entry.level + "] " + entry.text)
	}
	return out.String()
}

// imagesExpression lists the first maxImages images of the document with their
// natural size. The page can only return data; the expression never writes.
const imagesExpression = `Array.from(document.images).slice(0,100).map(function(i){` +
	`var s=i.currentSrc||i.src||"";var a=i.alt||"";` +
	`return {src:String(s).slice(0,2048),alt:String(a).slice(0,512),width:i.naturalWidth,height:i.naturalHeight};})`

// Image is one image of the active page (TOOL-009).
type Image struct {
	Src    string `json:"src"`
	Alt    string `json:"alt"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// Images lists the images of the current page, bounded by maxImages and
// MaxResult. An engine page answers through Eval; a page that returns
// something else than an array is a protocol failure.
func (p *Page) Images(ctx context.Context) ([]Image, error) {
	value, err := p.Eval(ctx, imagesExpression)
	if err != nil {
		return nil, err
	}
	var images []Image
	if err := json.Unmarshal([]byte(value), &images); err != nil {
		return nil, fmt.Errorf("%w: images: %w", errPage, err)
	}
	if len(images) > maxImages {
		images = images[:maxImages]
	}
	for i := range images {
		images[i].Src = clip(images[i].Src, maxImageSrc)
		images[i].Alt = clip(images[i].Alt, maxImageAlt)
	}
	return images, nil
}

// clip bounds s to max runes without splitting one.
func clip(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	out := make([]rune, 0, max)
	for _, r := range s {
		if len(out) == max {
			break
		}
		out = append(out, r)
	}
	return string(out)
}
