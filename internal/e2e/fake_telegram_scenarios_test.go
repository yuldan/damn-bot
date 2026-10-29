package e2e

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestFakeTelegramConversationInjection(t *testing.T) {
	conv := NewConversation(t, &FakeTGOptions{})
	ids := pushInjectedUpdates(conv)
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("update ids not monotonic: %v", ids)
		}
	}
	assertInjectedUpdates(t, mustConversationUpdates(t, conv))
}

func TestFakeTelegramConversationOffset(t *testing.T) {
	conv := NewConversation(t, &FakeTGOptions{})
	first := conv.Server.PushMessage(501, 11, "first", FakeTGUser{ID: 9001, FirstName: "Ana"})
	updates := mustConversationUpdates(t, conv)
	if len(updates) != 1 || updates[0].UpdateID != first {
		t.Fatalf("first batch = %+v (pushed id %d)", updates, first)
	}
	if none := mustConversationUpdates(t, conv); len(none) != 0 {
		t.Fatalf("acked update redelivered: %+v", none)
	}
	last := conv.Server.PushMessage(501, 11, "next", FakeTGUser{ID: 9001, FirstName: "Ana"})
	fresh := mustConversationUpdates(t, conv)
	if len(fresh) != 1 || fresh[0].UpdateID != last {
		t.Fatalf("fresh batch = %+v (pushed id %d)", fresh, last)
	}
}

// TestFakeTelegramConversationStart is the /start scenario for an unknown
// chat. The F4 gateway adapter is not in main yet (internal/gateway has only
// doc.go), so there is no policy to answer or refuse; skip instead of
// inventing one.
func TestFakeTelegramConversationStart(t *testing.T) {
	t.Skip("gateway adapter pending")
}

func TestFakeTelegramConversationUnknownMessage(t *testing.T) {
	t.Run("silent deny control", func(t *testing.T) { runUnknownMessageScenario(t, false, "", 0, 0) })
	t.Run("explicit refusal", func(t *testing.T) { runUnknownMessageScenario(t, false, "not authorized", 1, 0) })
	t.Run("known chat runs one tool", func(t *testing.T) { runUnknownMessageScenario(t, true, "", 0, 1) })
}

func TestFakeTelegramConversationTwoChats(t *testing.T) {
	t.Run("separate outbox per chat", runTwoChatsScenario)
	t.Run("control cross-chat edit is visible", runCrossChatControl)
}

func TestFakeTelegramConversationRateLimit(t *testing.T) {
	t.Run("backoff then deliver once", runRateLimitRetry)
	t.Run("control no retry keeps zero sends", runRateLimitNoRetry)
	t.Run("control duplicate send is visible", runDuplicateSendControl)
}

func TestFakeTelegramConversationApproval(t *testing.T) {
	t.Run("same callback applied once", runApprovalOnce)
	t.Run("control without dedupe answers twice", runApprovalNoDedupe)
}

func TestFakeTelegramConversationEditStreamRecovery(t *testing.T) {
	t.Run("close then retry does not duplicate", runEditCloseRetry)
	t.Run("drop then retry does not duplicate", runEditDropRetry)
	t.Run("control no retry leaves zero edits", runEditNoRetry)
	t.Run("control recovery by resend is visible", runEditResend)
}

func pushInjectedUpdates(conv *Conversation) []int64 {
	user := FakeTGUser{ID: 9001, FirstName: "Ana"}
	return []int64{
		conv.Server.PushMessage(501, 11, "hello", user),
		conv.Server.PushCommand(501, 11, "/start now", user),
		conv.Server.PushEdit(501, 11, 77, "edited", user),
		conv.Server.PushMedia(501, 11, FakeTGMedia{Kind: "photo", FileID: "photo-1", Caption: "pic"}, user),
		conv.Server.PushCallback(501, "approve:42"),
	}
}

func assertInjectedUpdates(t *testing.T, updates []FakeTGUpdate) {
	t.Helper()
	if len(updates) != 5 {
		t.Fatalf("updates = %d, want 5", len(updates))
	}
	assertTextUpdate(t, updates[0])
	assertCommandUpdate(t, updates[1])
	assertEditUpdate(t, updates[2])
	assertMediaUpdate(t, updates[3])
	assertCallbackUpdate(t, updates[4])
}

func assertTextUpdate(t *testing.T, update FakeTGUpdate) {
	t.Helper()
	if update.Message == nil || update.Message.MessageThreadID != 11 || update.Message.Text != "hello" {
		t.Fatalf("message update = %+v", update)
	}
}

func assertCommandUpdate(t *testing.T, update FakeTGUpdate) {
	t.Helper()
	msg := update.Message
	if msg == nil || len(msg.Entities) != 1 || msg.Entities[0].Type != "bot_command" || msg.Entities[0].Length != len("/start") {
		t.Fatalf("command update = %+v", update)
	}
}

func assertEditUpdate(t *testing.T, update FakeTGUpdate) {
	t.Helper()
	if update.EditedMessage == nil || update.EditedMessage.MessageID != 77 || update.EditedMessage.Text != "edited" {
		t.Fatalf("edit update = %+v", update)
	}
}

func assertMediaUpdate(t *testing.T, update FakeTGUpdate) {
	t.Helper()
	msg := update.Message
	if msg == nil || len(msg.Photo) != 1 || msg.Photo[0].FileID != "photo-1" || msg.Caption != "pic" {
		t.Fatalf("media update = %+v", update)
	}
}

func assertCallbackUpdate(t *testing.T, update FakeTGUpdate) {
	t.Helper()
	if update.CallbackQuery == nil || update.CallbackQuery.Data != "approve:42" {
		t.Fatalf("callback update = %+v", update)
	}
}

func mustConversationUpdates(t *testing.T, conv *Conversation) []FakeTGUpdate {
	t.Helper()
	updates, err := conv.Next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return updates
}

func runUnknownMessageScenario(t *testing.T, known bool, refusal string, wantSends, wantTools int) {
	t.Helper()
	const chatID = 9001
	conv := NewConversation(t, &FakeTGOptions{})
	tools := 0
	script := ConversationScript{
		Name: "unknown-message",
		Conv: conv,
		Steps: []ConversationStep{
			{Name: "inject", Run: func(_ context.Context, c *Conversation) error {
				c.Server.PushMessage(chatID, 0, "who are you?", FakeTGUser{ID: chatID, FirstName: "Eve"})
				return nil
			}},
			{Name: "handle", Run: func(ctx context.Context, c *Conversation) error {
				updates, err := c.Next(ctx)
				if err != nil {
					return err
				}
				if len(updates) != 1 || updates[0].Message == nil {
					return fmt.Errorf("updates = %+v, want one message", updates)
				}
				if known {
					tools++
					return nil
				}
				if refusal == "" {
					return nil
				}
				_, err = c.Send(ctx, chatID, refusal)
				return err
			}},
			{Name: "assert", Run: func(_ context.Context, c *Conversation) error {
				if err := c.Server.Drain(); err != nil {
					return err
				}
				if got := len(c.Server.Sends()); got != wantSends {
					return fmt.Errorf("sends = %d, want %d", got, wantSends)
				}
				if tools != wantTools {
					return fmt.Errorf("tool calls = %d, want %d", tools, wantTools)
				}
				return nil
			}},
		},
	}
	RunConversation(t, script)
}

func runTwoChatsScenario(t *testing.T) {
	t.Helper()
	const chatA, chatB = 1001, 1002
	conv := NewConversation(t, &FakeTGOptions{})
	var msgA, msgB FakeTGMessage
	script := ConversationScript{
		Name: "two-chats",
		Conv: conv,
		Steps: []ConversationStep{
			{Name: "inject two chats", Run: func(_ context.Context, c *Conversation) error {
				c.Server.PushMessage(chatA, 0, "alpha", FakeTGUser{ID: 1, FirstName: "A"})
				c.Server.PushMessage(chatB, 0, "beta", FakeTGUser{ID: 2, FirstName: "B"})
				return nil
			}},
			{Name: "reply per chat", Run: func(ctx context.Context, c *Conversation) error {
				updates, err := c.Next(ctx)
				if err != nil {
					return err
				}
				if len(updates) != 2 {
					return fmt.Errorf("updates = %d, want 2", len(updates))
				}
				msgA, err = c.Send(ctx, chatA, "alpha-ack")
				if err != nil {
					return err
				}
				msgB, err = c.Send(ctx, chatB, "beta-ack")
				if err != nil {
					return err
				}
				_, err = c.Server.WaitSend(chatA, func(req FakeTGRequest) bool { return req.Text == "alpha-ack" }, time.Second)
				return err
			}},
			{Name: "edit own message", Run: func(ctx context.Context, c *Conversation) error {
				if _, err := c.Edit(ctx, chatA, msgA.MessageID, "alpha-done"); err != nil {
					return err
				}
				_, err := c.Edit(ctx, chatB, msgB.MessageID, "beta-done")
				return err
			}},
			{Name: "assert separation", Run: func(_ context.Context, c *Conversation) error {
				if err := c.Server.Drain(); err != nil {
					return err
				}
				return separateOutboxErr(c.Server)
			}},
		},
	}
	RunConversation(t, script)
}

func runCrossChatControl(t *testing.T) {
	t.Helper()
	const chatA, chatB = 1001, 1002
	conv := NewConversation(t, &FakeTGOptions{})
	msgA, err := conv.Send(t.Context(), chatA, "to-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conv.Send(t.Context(), chatB, "to-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.Edit(t.Context(), chatA, msgA.MessageID, "ok"); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.Edit(t.Context(), chatB, msgA.MessageID, "wrong-chat"); err != nil {
		t.Fatal(err)
	}
	if err := conv.Server.Drain(); err != nil {
		t.Fatal(err)
	}
	if err := separateOutboxErr(conv.Server); err == nil {
		t.Fatal("cross-chat edit was not detected")
	}
}

func runRateLimitRetry(t *testing.T) {
	t.Helper()
	const chatID = 2001
	conv := NewConversation(t, &FakeTGOptions{Faults: map[string]FakeTGFault{
		"sendMessage": {Status: 429, RetryAfter: 1, Times: 1},
	}})
	var messageID int64
	script := ConversationScript{
		Name: "rate-limit",
		Conv: conv,
		Steps: []ConversationStep{
			{Name: "inject", Run: func(_ context.Context, c *Conversation) error {
				c.Server.PushMessage(chatID, 0, "give me one answer", FakeTGUser{ID: chatID, FirstName: "Ana"})
				return nil
			}},
			{Name: "send with backoff", Run: func(ctx context.Context, c *Conversation) error {
				if _, err := c.Next(ctx); err != nil {
					return err
				}
				msg, err := c.Send(ctx, chatID, "reply")
				if err != nil {
					if waitErr := waitRetryAfter(ctx, err); waitErr != nil {
						return waitErr
					}
					msg, err = c.Send(ctx, chatID, "reply")
				}
				if err != nil {
					return err
				}
				messageID = msg.MessageID
				return nil
			}},
			{Name: "stream edits", Run: func(ctx context.Context, c *Conversation) error {
				if _, err := c.Edit(ctx, chatID, messageID, "reply part 1"); err != nil {
					return err
				}
				_, err := c.Edit(ctx, chatID, messageID, "reply part 2")
				return err
			}},
			{Name: "assert single message", Run: func(_ context.Context, c *Conversation) error {
				if err := c.Server.Drain(); err != nil {
					return err
				}
				if err := singleSendErr(c.Server); err != nil {
					return err
				}
				if got := len(c.Server.EditsFor(messageID)); got != 2 {
					return fmt.Errorf("edits for message = %d, want 2", got)
				}
				if got := len(c.Server.RequestsFor("sendMessage")); got != 2 {
					return fmt.Errorf("send attempts = %d, want 2 (429 + retry)", got)
				}
				return nil
			}},
		},
	}
	RunConversation(t, script)
}

func runRateLimitNoRetry(t *testing.T) {
	t.Helper()
	conv := NewConversation(t, &FakeTGOptions{Faults: map[string]FakeTGFault{
		"sendMessage": {Status: 429, RetryAfter: 1, Times: 1},
	}})
	if _, err := conv.Send(t.Context(), 2001, "reply"); err == nil {
		t.Fatal("first send succeeded, want 429")
	}
	if err := conv.Server.Drain(); err != nil {
		t.Fatal(err)
	}
	if got := len(conv.Server.Sends()); got != 0 {
		t.Fatalf("sends = %d, want 0 without retry", got)
	}
}

func runDuplicateSendControl(t *testing.T) {
	t.Helper()
	conv := NewConversation(t, &FakeTGOptions{})
	if _, err := conv.Send(t.Context(), 2001, "reply"); err != nil {
		t.Fatal(err)
	}
	if _, err := conv.Send(t.Context(), 2001, "reply"); err != nil {
		t.Fatal(err)
	}
	if err := conv.Server.Drain(); err != nil {
		t.Fatal(err)
	}
	if err := singleSendErr(conv.Server); err == nil {
		t.Fatal("duplicate send was not detected")
	}
}

func runApprovalOnce(t *testing.T) {
	t.Helper()
	const chatID = 3001
	conv := NewConversation(t, &FakeTGOptions{})
	applied := map[string]int{}
	answered := map[string]bool{}
	var queries []*FakeTGCallbackQuery
	script := ConversationScript{
		Name: "approval-idempotent",
		Conv: conv,
		Steps: []ConversationStep{
			{Name: "inject callback twice", Run: func(ctx context.Context, c *Conversation) error {
				c.Server.PushCallback(chatID, "approve:42")
				updates, err := c.Next(ctx)
				if err != nil {
					return err
				}
				if len(updates) != 1 || updates[0].CallbackQuery == nil {
					return fmt.Errorf("updates = %+v, want one callback", updates)
				}
				queries = append(queries, updates[0].CallbackQuery)
				c.Server.PushUpdate(FakeTGUpdate{CallbackQuery: updates[0].CallbackQuery})
				return nil
			}},
			{Name: "apply idempotently", Run: func(ctx context.Context, c *Conversation) error {
				updates, err := c.Next(ctx)
				if err != nil {
					return err
				}
				if len(updates) != 1 || updates[0].CallbackQuery == nil {
					return fmt.Errorf("redelivered updates = %+v", updates)
				}
				queries = append(queries, updates[0].CallbackQuery)
				return applyCallbacks(ctx, c, queries, answered, applied)
			}},
			{Name: "assert once", Run: func(_ context.Context, c *Conversation) error {
				if err := c.Server.Drain(); err != nil {
					return err
				}
				if got := len(c.Server.CallbackAnswers()); got != 1 {
					return fmt.Errorf("callback answers = %d, want 1", got)
				}
				if got := applied["approve:42"]; got != 1 {
					return fmt.Errorf("decision applications = %d, want 1", got)
				}
				return nil
			}},
		},
	}
	RunConversation(t, script)
}

func applyCallbacks(ctx context.Context, conv *Conversation, queries []*FakeTGCallbackQuery, answered map[string]bool, applied map[string]int) error {
	for _, query := range queries {
		if answered[query.ID] {
			continue
		}
		if err := conv.AnswerCallback(ctx, query.ID); err != nil {
			return err
		}
		answered[query.ID] = true
		applied[query.Data]++
	}
	return nil
}

func runApprovalNoDedupe(t *testing.T) {
	t.Helper()
	const chatID = 3001
	conv := NewConversation(t, &FakeTGOptions{})
	conv.Server.PushCallback(chatID, "approve:42")
	updates, err := conv.Next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if answerErr := conv.AnswerCallback(t.Context(), updates[0].CallbackQuery.ID); answerErr != nil {
		t.Fatal(answerErr)
	}
	conv.Server.PushUpdate(FakeTGUpdate{CallbackQuery: updates[0].CallbackQuery})
	updates, err = conv.Next(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for i := range updates {
		if answerErr := conv.AnswerCallback(t.Context(), updates[i].CallbackQuery.ID); answerErr != nil {
			t.Fatal(answerErr)
		}
	}
	if err := conv.Server.Drain(); err != nil {
		t.Fatal(err)
	}
	if got := len(conv.Server.CallbackAnswers()); got != 2 {
		t.Fatalf("callback answers = %d, want 2 without dedupe", got)
	}
}

func runEditCloseRetry(t *testing.T) {
	t.Helper()
	const chatID = 4001
	conv := NewConversation(t, &FakeTGOptions{Faults: map[string]FakeTGFault{
		"editMessageText": {Close: true, Times: 1},
	}})
	var messageID int64
	script := ConversationScript{
		Name: "edit-close",
		Conv: conv,
		Steps: []ConversationStep{
			{Name: "send once", Run: func(ctx context.Context, c *Conversation) error {
				c.Server.PushMessage(chatID, 0, "start", FakeTGUser{ID: chatID, FirstName: "Ana"})
				if _, err := c.Next(ctx); err != nil {
					return err
				}
				msg, err := c.Send(ctx, chatID, "streaming")
				if err != nil {
					return err
				}
				messageID = msg.MessageID
				return nil
			}},
			{Name: "edit with retry", Run: func(ctx context.Context, c *Conversation) error {
				if _, err := c.Edit(ctx, chatID, messageID, "streaming part 1"); err == nil {
					return errors.New("first edit succeeded, want dropped connection")
				}
				if _, err := c.Edit(ctx, chatID, messageID, "streaming part 2"); err != nil {
					return err
				}
				_, err := c.Server.WaitEdit(chatID, func(req FakeTGRequest) bool {
					return req.MessageID == messageID && req.Text == "streaming part 2"
				}, time.Second)
				return err
			}},
			{Name: "assert no duplicate", Run: func(_ context.Context, c *Conversation) error {
				if err := c.Server.Drain(); err != nil {
					return err
				}
				if err := singleSendErr(c.Server); err != nil {
					return err
				}
				if got := len(c.Server.EditsFor(messageID)); got != 1 {
					return fmt.Errorf("edits for message = %d, want 1", got)
				}
				if got := len(c.Server.RequestsFor("editMessageText")); got != 2 {
					return fmt.Errorf("edit attempts = %d, want 2 (close + retry)", got)
				}
				return nil
			}},
		},
	}
	RunConversation(t, script)
}

func runEditDropRetry(t *testing.T) {
	t.Helper()
	const chatID = 4001
	conv := NewConversation(t, &FakeTGOptions{Faults: map[string]FakeTGFault{
		"editMessageText": {Drop: true, Times: 1},
	}})
	message, err := conv.Send(t.Context(), chatID, "streaming")
	if err != nil {
		t.Fatal(err)
	}
	dropCtx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	_, dropErr := conv.Edit(dropCtx, chatID, message.MessageID, "part 1")
	cancel()
	if !errors.Is(dropErr, context.DeadlineExceeded) {
		t.Fatalf("dropped edit error = %v, want context deadline", dropErr)
	}
	if _, err := conv.Edit(t.Context(), chatID, message.MessageID, "part 2"); err != nil {
		t.Fatal(err)
	}
	if err := conv.Server.Drain(); err != nil {
		t.Fatal(err)
	}
	if err := singleSendErr(conv.Server); err != nil {
		t.Fatal(err)
	}
	if got := len(conv.Server.EditsFor(message.MessageID)); got != 1 {
		t.Fatalf("edits for message = %d, want 1", got)
	}
}

func runEditNoRetry(t *testing.T) {
	t.Helper()
	const chatID = 4001
	conv := NewConversation(t, &FakeTGOptions{Faults: map[string]FakeTGFault{
		"editMessageText": {Close: true, Times: 1},
	}})
	message, err := conv.Send(t.Context(), chatID, "streaming")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conv.Edit(t.Context(), chatID, message.MessageID, "part 1"); err == nil {
		t.Fatal("edit succeeded, want dropped connection")
	}
	if err := conv.Server.Drain(); err != nil {
		t.Fatal(err)
	}
	if got := len(conv.Server.Edits()); got != 0 {
		t.Fatalf("edits = %d, want 0 without retry", got)
	}
}

func runEditResend(t *testing.T) {
	t.Helper()
	const chatID = 4001
	conv := NewConversation(t, &FakeTGOptions{Faults: map[string]FakeTGFault{
		"editMessageText": {Close: true, Times: 1},
	}})
	message, err := conv.Send(t.Context(), chatID, "streaming")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conv.Edit(t.Context(), chatID, message.MessageID, "part 1"); err == nil {
		t.Fatal("edit succeeded, want dropped connection")
	}
	if _, err := conv.Send(t.Context(), chatID, "resend"); err != nil {
		t.Fatal(err)
	}
	if err := conv.Server.Drain(); err != nil {
		t.Fatal(err)
	}
	if err := singleSendErr(conv.Server); err == nil {
		t.Fatal("orphan resend was not detected")
	}
}

func separateOutboxErr(s *FakeTG) error {
	sends := s.Sends()
	if len(sends) != 2 {
		return fmt.Errorf("sends = %d, want 2", len(sends))
	}
	byMessage := make(map[int64]FakeTGRequest, len(sends))
	chats := make(map[int64]struct{}, len(sends))
	for i := range sends {
		send := &sends[i]
		byMessage[send.ResultMessageID] = *send
		chats[send.ChatID] = struct{}{}
	}
	if len(chats) != 2 {
		return fmt.Errorf("send chats = %v, want two", chats)
	}
	edits := s.Edits()
	if len(edits) != 2 {
		return fmt.Errorf("edits = %d, want 2", len(edits))
	}
	for i := range edits {
		edit := &edits[i]
		send, ok := byMessage[edit.MessageID]
		if !ok {
			return fmt.Errorf("edit for unknown message %d", edit.MessageID)
		}
		if send.ChatID != edit.ChatID {
			return fmt.Errorf("edit for message %d went to chat %d, sent to chat %d", edit.MessageID, edit.ChatID, send.ChatID)
		}
	}
	return nil
}

func singleSendErr(s *FakeTG) error {
	if got := len(s.Sends()); got != 1 {
		return fmt.Errorf("sends = %d, want 1", got)
	}
	return nil
}

func waitRetryAfter(ctx context.Context, err error) error {
	var apiErr FakeTGAPIError
	if !errors.As(err, &apiErr) {
		return fmt.Errorf("not a Telegram API error: %w", err)
	}
	if apiErr.RetryAfter <= 0 {
		return fmt.Errorf("retry_after = %d, want positive", apiErr.RetryAfter)
	}
	timer := time.NewTimer(time.Duration(apiErr.RetryAfter) * time.Second)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait retry-after: %w", ctx.Err())
	}
}
