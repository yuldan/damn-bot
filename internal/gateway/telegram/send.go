package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"sync"

	"github.com/damnbot/damnbot/internal/gateway/render"
)

// Outbound is one message. Key is the outbox idempotency key: a retry with the
// same key resolves to the message already sent instead of sending a duplicate.
// Anchor is an optional reply target; an invalid anchor is dropped so the send
// still succeeds without reply_to_message_id (EC-PLAT-018). Media, when set,
// is uploaded as a photo or document and Text becomes its caption.
type Outbound struct {
	ChatID, ThreadID, Text, Key, Anchor string
	Media                               *Media
}

// Send renders markdown to plain text plus entities and delivers it, splitting
// replies over MaxLen UTF-16 units into consecutive messages. It returns the
// first message id; a retry with Key resumes the chunks already sent.
func (a *Adapter) Send(ctx context.Context, out *Outbound) (int64, error) {
	if out == nil || out.ChatID == "" || (out.Text == "" && out.Media == nil) {
		return 0, errors.New("telegram: send requires chat and text or media")
	}
	if out.Media != nil {
		return a.deliverMedia(ctx, out)
	}
	msgs := renderMessages(out.Text)
	return a.deliverChunks(ctx, out, msgs)
}

// renderMessages is the one renderer entry point for sends, edits and cards:
// an empty render (whitespace-only source) falls back to escaped text so the
// turn still shows something instead of failing.
func renderMessages(src string) []render.Message {
	if msgs := render.RenderTelegram(src, render.Limits{}); len(msgs) > 0 {
		return msgs
	}
	return []render.Message{{Text: render.Escape(src)}}
}

// deliverChunks sends rendered chunks in order, pacing every chunk per chat and
// globally. Chunk 0 keeps the outbox key and the reply anchor; later chunks
// derive "<key>#<n>" so a retry skips what already reached Telegram.
func (a *Adapter) deliverChunks(ctx context.Context, out *Outbound, msgs []render.Message) (int64, error) {
	anchor := anchorField(out.Anchor)
	var first int64
	for i, msg := range msgs {
		key := ""
		if out.Key != "" {
			key = chunkKey(out.Key, i)
		}
		if i > 0 {
			anchor = "" // a reply target belongs to the first message only
		}
		id, _, err := a.deliverOne(ctx, out.ChatID, out.ThreadID, anchor, msg, key)
		if err != nil {
			return 0, err
		}
		if i == 0 {
			first = id
		}
	}
	return first, nil
}

// deliverOne sends one rendered message under key unless the store already has
// it; fresh reports whether this call performed the send.
func (a *Adapter) deliverOne(ctx context.Context, chatID, threadID, anchor string, msg render.Message, key string) (int64, bool, error) {
	stored, found, loadErr := a.lookup(ctx, key)
	if loadErr != nil {
		return 0, false, loadErr
	}
	if found {
		return stored, false, nil
	}
	if waitErr := a.send.wait(ctx, chatID); waitErr != nil {
		return 0, false, waitErr
	}
	id, sendErr := a.sendMessage(ctx, chatID, threadID, anchor, msg, "")
	if sendErr != nil {
		if key != "" && uncertain(sendErr) {
			if perr := a.store.PutMessageID(ctx, key, 0); perr != nil {
				return 0, false, fmt.Errorf("telegram: store uncertain marker: %w", perr)
			}
		}
		return 0, false, sendErr
	}
	if key != "" {
		if storeErr := a.store.PutMessageID(ctx, key, id); storeErr != nil {
			return 0, false, fmt.Errorf("telegram: store message id: %w", storeErr)
		}
	}
	return id, true, nil
}

// lookup resolves an idempotency key before a send: found reports that the
// message already exists, and a stored zero is the uncertain outcome.
func (a *Adapter) lookup(ctx context.Context, key string) (int64, bool, error) {
	if key == "" {
		return 0, false, nil
	}
	stored, found, err := a.store.MessageID(ctx, key)
	if err != nil {
		return 0, false, fmt.Errorf("telegram: load message id: %w", err)
	}
	if found && stored == 0 {
		return 0, false, ErrUncertain
	}
	return stored, found, nil
}

// chunkKey derives the per-chunk idempotency key.
func chunkKey(key string, i int) string {
	if i == 0 {
		return key
	}
	return key + "#" + strconv.Itoa(i)
}

// anchorField validates a reply anchor; an empty result omits the form field,
// so a bogus id never fails the send (EC-PLAT-018).
func anchorField(raw string) string {
	id, ok := render.ParseAnchor(raw)
	if !ok {
		return ""
	}
	return strconv.FormatInt(id, 10)
}

func uncertain(err error) bool {
	if ae, ok := errors.AsType[*apiError](err); ok {
		return ae.Code >= 500 || ae.Code == 409
	}
	return true // transport failure: the request may have been processed
}

// Typing sends the `typing` chat action. It is best effort by design: the
// caller repeats it while the turn runs and ignores every error, so a rejected
// or rate-limited action never fails a turn. State is not persisted.
func (a *Adapter) Typing(ctx context.Context, chatID, threadID string) error {
	if chatID == "" {
		return errors.New("telegram: typing requires a chat")
	}
	form := url.Values{"chat_id": {chatID}, "action": {"typing"}}
	if threadID != "" {
		form.Set("message_thread_id", threadID)
	}
	var ok bool
	if err := a.call(ctx, "sendChatAction", form, &ok); err != nil {
		return err
	}
	return nil
}

// Stream edits one message; updates during an edit coalesce. A reply longer
// than one message keeps editing the first message and appends the remaining
// chunks once, editing them in place on later updates.
type Stream struct {
	a       *Adapter
	out     Outbound
	mu      sync.Mutex
	busy    sync.Mutex
	latest  string
	sent    string
	msgID   int64
	extras  []int64
	started bool
}

// Stream opens a streaming reply; no I/O happens until the first Update.
func (a *Adapter) Stream(out *Outbound) *Stream {
	return &Stream{a: a, out: *out}
}

// StreamFrom opens a streaming reply on an existing message (the instant
// placeholder), so every update edits it instead of sending a second message.
// shown is the text currently on screen; the first Update edits away from it.
func (a *Adapter) StreamFrom(out *Outbound, messageID int64, shown string) *Stream {
	return &Stream{a: a, out: *out, msgID: messageID, started: true, sent: shown}
}

// Update records text and flushes it: the first call sends, later calls edit.
func (s *Stream) Update(ctx context.Context, text string) error {
	s.mu.Lock()
	s.latest = text
	s.mu.Unlock()
	if !s.busy.TryLock() {
		return nil // an in-flight flush picks up the newer text
	}
	defer s.busy.Unlock()
	for {
		s.mu.Lock()
		text, sent := s.latest, s.sent
		s.mu.Unlock()
		if text == "" || text == sent {
			return nil
		}
		if err := s.push(ctx, text); err != nil {
			return err
		}
	}
}

func (s *Stream) push(ctx context.Context, text string) error {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	for i, msg := range renderMessages(text) {
		var err error
		if i == 0 {
			err = s.pushFirst(ctx, msg, started)
		} else {
			err = s.pushExtra(ctx, i, msg)
		}
		if err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.sent = text
	s.mu.Unlock()
	return nil
}

func (s *Stream) pushFirst(ctx context.Context, msg render.Message, started bool) error {
	s.mu.Lock()
	msgID := s.msgID
	s.mu.Unlock()
	if !started {
		id, fresh, err := s.a.deliverOne(ctx, s.out.ChatID, s.out.ThreadID, anchorField(s.out.Anchor), msg, s.out.Key)
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.msgID, s.started = id, true
		s.mu.Unlock()
		if !fresh { // a retry found the message already sent; bring it current
			return s.replace(ctx, id, msg)
		}
		return nil
	}
	return s.replace(ctx, msgID, msg)
}

// pushExtra appends chunk i once, then edits that message on later updates.
func (s *Stream) pushExtra(ctx context.Context, i int, msg render.Message) error {
	key := ""
	if s.out.Key != "" {
		key = chunkKey(s.out.Key, i)
	}
	s.mu.Lock()
	switch {
	case i-1 < len(s.extras):
		id := s.extras[i-1]
		s.mu.Unlock()
		return s.replace(ctx, id, msg)
	case key != "":
		s.mu.Unlock()
		id, fresh, err := s.a.deliverOne(ctx, s.out.ChatID, s.out.ThreadID, "", msg, key)
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.extras = append(s.extras, id)
		s.mu.Unlock()
		if !fresh {
			return s.replace(ctx, id, msg)
		}
		return nil
	default:
		s.mu.Unlock()
		id, _, err := s.a.deliverOne(ctx, s.out.ChatID, s.out.ThreadID, "", msg, "")
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.extras = append(s.extras, id)
		s.mu.Unlock()
		return nil
	}
}

// replace edits one streamed message, pacing edits per message. A 429 is
// waited out once (the penalty already blocks the chat's other sends), so the
// final text lands in the same message instead of a duplicate (EC-PLAT-014).
func (s *Stream) replace(ctx context.Context, messageID int64, msg render.Message) error {
	key := "edit:" + s.out.ChatID + ":" + strconv.FormatInt(messageID, 10)
	if err := s.a.edit.wait(ctx, key); err != nil {
		return err
	}
	err := s.a.editText(ctx, s.out.ChatID, s.out.ThreadID, messageID, msg)
	wait, throttled := s.a.penalize(s.out.ChatID, err)
	if !throttled {
		return err
	}
	if werr := s.a.clock.Wait(ctx, wait); werr != nil {
		return fmt.Errorf("telegram: retry_after wait: %w", werr)
	}
	return s.a.editText(ctx, s.out.ChatID, s.out.ThreadID, messageID, msg)
}
