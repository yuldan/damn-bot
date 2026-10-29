package telegram

import "testing"

type mentionCase struct {
	name                                  string
	msg                                   map[string]any
	group, mentioned, reply, command, bot bool
}

func mentionCases(group map[string]any, from func(id int64, isBot bool) map[string]any, mention func(offset int) []any, botID int64) []mentionCase {
	return []mentionCase{
		{
			name:  "group without mention",
			msg:   map[string]any{"from": from(7, false), "chat": group, "text": "halo"},
			group: true,
		},
		{
			name:      "group with @username mention",
			msg:       map[string]any{"from": from(7, false), "chat": group, "text": "@damnbot halo", "entities": mention(0)},
			group:     true,
			mentioned: true,
		},
		{
			// The emoji is two UTF-16 code units, so the mention starts at 3.
			name:      "mention after emoji uses utf16 offsets",
			msg:       map[string]any{"from": from(7, false), "chat": group, "text": "🙂 @damnbot", "entities": mention(3)},
			group:     true,
			mentioned: true,
		},
		{
			name: "text_mention of this bot",
			msg: map[string]any{"from": from(7, false), "chat": group, "text": "halo", "entities": []any{
				map[string]any{"type": "text_mention", "offset": 0, "length": 4, "user": map[string]any{"id": botID}},
			}},
			group:     true,
			mentioned: true,
		},
		{
			name:  "mention of another user is not ours",
			msg:   map[string]any{"from": from(7, false), "chat": group, "text": "@other halo", "entities": mention(0)},
			group: true,
		},
		{
			name:  "another bot is flagged as a bot",
			msg:   map[string]any{"from": from(9, true), "chat": group, "text": "halo"},
			group: true,
			bot:   true,
		},
		{
			name: "reply to this bot",
			msg: map[string]any{
				"from": from(7, false), "chat": group, "text": "ya",
				"reply_to_message": map[string]any{"message_id": 1, "from": from(botID, true), "chat": group},
			},
			group: true,
			reply: true,
		},
		{
			name: "command for this bot",
			msg: map[string]any{
				"from": from(7, false), "chat": group, "text": "/new",
				"entities": []any{map[string]any{"type": "bot_command", "offset": 0, "length": 4}},
			},
			group:   true,
			command: true,
		},
		{
			name: "command for another bot",
			msg: map[string]any{
				"from": from(7, false), "chat": group, "text": "/new@other",
				"entities": []any{map[string]any{"type": "bot_command", "offset": 0, "length": 10}},
			},
			group: true,
		},
		{
			name: "direct message",
			msg:  map[string]any{"from": from(7, false), "chat": map[string]any{"id": 7, "type": "private"}, "text": "halo"},
		},
	}
}

// TestInboundMentionFacts proves the adapter fills the transport facts the
// core mention policy needs (GAP-GW-05, EC-PLAT-022) and that a @username
// mention is read with UTF-16 offsets (EC-PLAT-011).
func TestInboundMentionFacts(t *testing.T) {
	const botID = int64(424242) // numeric prefix of fakeTG.token
	group := map[string]any{"id": -100123, "type": "supergroup"}
	from := func(id int64, isBot bool) map[string]any {
		return map[string]any{"id": id, "is_bot": isBot}
	}
	mention := func(offset int) []any {
		return []any{map[string]any{"type": "mention", "offset": offset, "length": 8}}
	}
	for _, tc := range mentionCases(group, from, mention, botID) {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeTG(t)
			a, _ := newTestAdapter(t, f, newMemStore(), func(cfg *Config) { cfg.Username = "@damnbot" })
			if a.account != "424242" {
				t.Fatalf("account = %q, want token prefix", a.account)
			}
			in := make(chan Inbound, 8)
			done, stop := runAdapter(t, a, in)
			f.pushMessage(t, tc.msg)
			ib := recvInbound(t, in)
			stop()
			if err := <-done; err != nil {
				t.Fatalf("Run: %v", err)
			}
			if ib.Account != "424242" {
				t.Errorf("inbound account = %q", ib.Account)
			}
			got := [5]bool{ib.Group, ib.Mentioned, ib.ReplyToBot, ib.Command, ib.SenderIsBot}
			want := [5]bool{tc.group, tc.mentioned, tc.reply, tc.command, tc.bot}
			if got != want {
				t.Errorf("facts (group, mentioned, reply, command, bot) = %v, want %v", got, want)
			}
		})
	}
}
