package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/gateway/telegram"
	"github.com/damnbot/damnbot/internal/loop/event"
)

// placeholderAPI is a Bot API that hands out message ids and records edits.
type placeholderAPI struct {
	next      atomic.Int64
	failEdits bool
	mu        sync.Mutex
	edits     []string
	ids       []int64
}

func (p *placeholderAPI) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(r.URL.Path, "/sendMessage"):
		id := p.next.Add(1)
		if _, err := io.WriteString(w, `{"ok":true,"result":{"message_id":`+strconv.FormatInt(id, 10)+`}}`); err != nil {
			return
		}
	case strings.HasSuffix(r.URL.Path, "/editMessageText"):
		if p.failEdits {
			w.WriteHeader(http.StatusInternalServerError)
			if _, err := io.WriteString(w, `{"ok":false,"error_code":500,"description":"boom"}`); err != nil {
				return
			}
			return
		}
		if err := r.ParseForm(); err != nil {
			return
		}
		id, perr := strconv.ParseInt(r.Form.Get("message_id"), 10, 64)
		if perr != nil {
			return
		}
		p.mu.Lock()
		p.edits = append(p.edits, r.Form.Get("text"))
		p.ids = append(p.ids, id)
		p.mu.Unlock()
		if _, err := io.WriteString(w, `{"ok":true,"result":true}`); err != nil {
			return
		}
	default:
		if _, err := io.WriteString(w, `{"ok":true,"result":[]}`); err != nil {
			return
		}
	}
}

func (p *placeholderAPI) recorded() ([]string, []int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.edits...), append([]int64(nil), p.ids...)
}

func newPlaceholderGateway(t *testing.T) (*serveGateway, *placeholderAPI) {
	t.Helper()
	api := &placeholderAPI{}
	srv := httptest.NewServer(http.HandlerFunc(api.handle))
	t.Cleanup(srv.Close)
	return newTestGateway(t, srv.URL), api
}

// TestStreamReplyEmptyAnswerReplacesPlaceholder: a turn with no text never
// leaves "…" behind; it edits the placeholder into the no-output notice.
func TestStreamReplyEmptyAnswerReplacesPlaceholder(t *testing.T) {
	gw, api := newPlaceholderGateway(t)
	in := &telegram.Inbound{Platform: "telegram", ChatID: "7788", SenderID: "7788", UpdateID: "u:1", Text: "hi"}
	ack := gw.placeholder(t.Context(), in)
	if ack == nil {
		t.Fatal("placeholder was not sent")
	}
	ch := make(chan event.Update)
	close(ch)
	if err := gw.streamReply(t.Context(), in, ch, nil, ack); err != nil {
		t.Fatalf("streamReply: %v", err)
	}
	edits, ids := api.recorded()
	if len(edits) != 1 || edits[0] != gatewayNoOutput {
		t.Fatalf("edits = %v, want one %q", edits, gatewayNoOutput)
	}
	if ids[0] != ack.id {
		t.Fatalf("edited message %d, want placeholder %d", ids[0], ack.id)
	}
}

// TestStreamReplyFailedEditGoesToOutbox: when the final edit fails, the text
// still reaches the durable outbox under a key that is not the placeholder's
// message-id key, so the retry really sends it.
func TestStreamReplyFailedEditGoesToOutbox(t *testing.T) {
	gw, api := newPlaceholderGateway(t)
	api.failEdits = true
	in := &telegram.Inbound{Platform: "telegram", ChatID: "7788", SenderID: "7788", UpdateID: "u:3", Text: "hi"}
	ack := gw.placeholder(t.Context(), in)
	if ack == nil {
		t.Fatal("placeholder was not sent")
	}
	ch := make(chan event.Update, 1)
	ch <- event.Update{Kind: event.UpdateTextDelta, Text: "answer"}
	close(ch)
	if err := gw.streamReply(t.Context(), in, ch, nil, ack); err != nil {
		t.Fatalf("streamReply: %v", err)
	}
	var body, idem string
	err := gw.store.DB.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT body, idem_key FROM gateway_deliveries WHERE chat_id = ?`, []any{"7788"}, &body, &idem)
	})
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if body != "answer" || !strings.Contains(idem, "final:u:3") {
		t.Fatalf("outbox row = %q / %q, want the answer under a final: key", body, idem)
	}
}

// TestFailPlaceholderRewritesInsteadOfResending: a turn that dies before any
// reply update turns the placeholder into the error text, without a new send.
func TestFailPlaceholderRewritesInsteadOfResending(t *testing.T) {
	gw, api := newPlaceholderGateway(t)
	in := &telegram.Inbound{Platform: "telegram", ChatID: "7788", SenderID: "7788", UpdateID: "u:2", Text: "hi"}
	ack := gw.placeholder(t.Context(), in)
	if ack == nil {
		t.Fatal("placeholder was not sent")
	}
	gw.failPlaceholder(t.Context(), in, ack)
	edits, ids := api.recorded()
	if len(edits) != 1 || edits[0] != gatewayTurnFailed {
		t.Fatalf("edits = %v, want one %q", edits, gatewayTurnFailed)
	}
	if ids[0] != ack.id {
		t.Fatalf("edited message %d, want placeholder %d", ids[0], ack.id)
	}
}
