package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"sync"
)

// Outbound is one message. Key is the outbox idempotency key: a retry with
type Outbound struct {
	ChatID, ThreadID, Text, Key string
}

// Send delivers one message, paced per chat and globally, and returns its
func (a *Adapter) Send(ctx context.Context, out Outbound) (int64, error) { //nolint:gocognit // key lookup, uncertainty and delivery are one path
	if out.ChatID == "" || out.Text == "" {
		return 0, errors.New("telegram: send requires chat and text")
	}
	if out.Key != "" { //nolint:nestif // the idempotency lookup must precede the send
		id, found, err := a.store.MessageID(ctx, out.Key)
		if err != nil {
			return 0, fmt.Errorf("telegram: load message id: %w", err)
		}
		if found {
			if id == 0 {
				return 0, ErrUncertain
			}
			return id, nil
		}
	}
	if err := a.send.wait(ctx, out.ChatID); err != nil {
		return 0, err
	}
	id, err := a.sendMessage(ctx, out.ChatID, out.ThreadID, out.Text, "")
	if err != nil {
		if out.Key != "" && uncertain(err) {
			if perr := a.store.PutMessageID(ctx, out.Key, 0); perr != nil {
				return 0, fmt.Errorf("telegram: store uncertain marker: %w", perr)
			}
		}
		return 0, err
	}
	if out.Key != "" {
		if err := a.store.PutMessageID(ctx, out.Key, id); err != nil {
			return 0, fmt.Errorf("telegram: store message id: %w", err)
		}
	}
	return id, nil
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

// Stream edits one message; updates during an edit coalesce.
type Stream struct {
	a       *Adapter
	out     Outbound
	mu      sync.Mutex
	busy    sync.Mutex
	latest  string
	sent    string
	msgID   int64
	started bool
}

// Stream opens a streaming reply; no I/O happens until the first Update.
func (a *Adapter) Stream(out Outbound) *Stream { return &Stream{a: a, out: out} }

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
	started, msgID := s.started, s.msgID
	s.mu.Unlock()
	if !started {
		id, err := s.a.Send(ctx, Outbound{ChatID: s.out.ChatID, ThreadID: s.out.ThreadID, Text: text, Key: s.out.Key})
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.msgID, s.started, s.sent = id, true, text
		s.mu.Unlock()
		return nil
	}
	if err := s.a.edit.wait(ctx, "edit:"+s.out.ChatID+":"+strconv.FormatInt(msgID, 10)); err != nil {
		return err
	}
	if err := s.a.editText(ctx, s.out.ChatID, s.out.ThreadID, msgID, text); err != nil {
		return err
	}
	s.mu.Lock()
	s.sent = text
	s.mu.Unlock()
	return nil
}
