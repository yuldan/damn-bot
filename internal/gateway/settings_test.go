package gateway_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
)

type fakeSettings struct {
	chat             map[string]string
	platform, chatID string
	thread           string
	hasHome          bool
	sets             int
}

func (f *fakeSettings) ChatSetting(_ context.Context, platform, chatID, key string) (string, bool, error) {
	v, ok := f.chat[platform+":"+chatID+"."+key]
	return v, ok, nil
}

func (f *fakeSettings) SetChatSetting(_ context.Context, platform, chatID, key, value string, _ int64) error {
	if f.chat == nil {
		f.chat = map[string]string{}
	}
	f.chat[platform+":"+chatID+"."+key] = value
	f.sets++
	return nil
}

func (f *fakeSettings) HomeChannel(context.Context) (string, string, string, bool, error) {
	return f.platform, f.chatID, f.thread, f.hasHome, nil
}

func (f *fakeSettings) SetHomeChannel(_ context.Context, platform, chatID, threadID string, _ int64) error {
	f.platform, f.chatID, f.thread, f.hasHome = platform, chatID, threadID, true
	f.sets++
	return nil
}

// TestSethomeIsIdempotentAndOwnerOnly pins GW-005/SLASH-043.
func TestSethomeIsIdempotentAndOwnerOnly(t *testing.T) {
	fs := &fakeSettings{}
	c := gateway.NewCommands(&gateway.CommandOptions{Settings: fs})
	owner := testTicket(gateway.RoleOwner)
	owner.Scope.ThreadID = "42"

	if res := c.Dispatch(context.Background(), owner, "/sethome"); !res.Handled || !strings.Contains(res.Reply, "telegram:c1/42") {
		t.Fatalf("/sethome = %+v", res)
	}
	if fs.sets != 1 || fs.chatID != "c1" || fs.thread != "42" {
		t.Fatalf("stored home = %s:%s/%s, sets = %d", fs.platform, fs.chatID, fs.thread, fs.sets)
	}
	if res := c.Dispatch(context.Background(), owner, "/sethome"); !strings.Contains(res.Reply, "already") {
		t.Fatalf("second /sethome = %+v, want already", res)
	}
	if fs.sets != 1 {
		t.Fatalf("idempotent /sethome wrote %d times", fs.sets)
	}
	if res := c.Dispatch(context.Background(), testTicket(gateway.RoleUser), "/sethome"); !strings.Contains(res.Reply, "owner-only") {
		t.Fatalf("/sethome as user = %+v", res)
	}
}

// TestFooterToggle renders off by default, then on/off/status per chat.
func TestFooterToggle(t *testing.T) {
	fs := &fakeSettings{}
	c := gateway.NewCommands(&gateway.CommandOptions{Settings: fs, Now: func() time.Time { return time.Unix(100, 0) }})
	ticket := testTicket(gateway.RoleOwner)
	if res := c.Dispatch(context.Background(), ticket, "/footer"); !strings.Contains(res.Reply, "off (default)") {
		t.Fatalf("/footer default = %+v", res)
	}
	if res := c.Dispatch(context.Background(), ticket, "/footer on"); !strings.Contains(res.Reply, "footer on") {
		t.Fatalf("/footer on = %+v", res)
	}
	if fs.chat["telegram:c1.footer"] != "true" {
		t.Fatalf("stored footer = %q", fs.chat["telegram:c1.footer"])
	}
	if res := c.Dispatch(context.Background(), ticket, "/footer status"); !strings.Contains(res.Reply, "footer on") {
		t.Fatalf("/footer status = %+v", res)
	}
	if res := c.Dispatch(context.Background(), ticket, "/footer off"); !strings.Contains(res.Reply, "footer off") {
		t.Fatalf("/footer off = %+v", res)
	}
	if res := c.Dispatch(context.Background(), testTicket(gateway.RoleUser), "/footer on"); !strings.Contains(res.Reply, "owner-only") {
		t.Fatalf("/footer as user = %+v", res)
	}
}

// TestTopicFailsClosedWithoutThread pins SLASH-003: no silent parent fallback.
func TestTopicFailsClosedWithoutThread(t *testing.T) {
	rt := &fakeRuntime{topicID: "s-topic"}
	c := gateway.NewCommands(&gateway.CommandOptions{Runtime: rt})
	res := c.Dispatch(context.Background(), testTicket(gateway.RoleOwner), "/topic")
	if !res.Handled || !strings.Contains(res.Reply, "no topic thread") {
		t.Fatalf("/topic without thread = %+v", res)
	}
	if len(rt.topicScopes) != 0 {
		t.Fatalf("runtime was asked for a session without a thread: %+v", rt.topicScopes)
	}
	ticket := testTicket(gateway.RoleOwner)
	ticket.Scope.ThreadID = "77"
	res = c.Dispatch(context.Background(), ticket, "/topic")
	if !strings.Contains(res.Reply, "topic 77: session s-topic") || len(rt.topicScopes) != 1 {
		t.Fatalf("/topic with thread = %+v scopes=%+v", res, rt.topicScopes)
	}
}

// TestStatusShowsHomeChannel pins the GW-005 /status surface.
func TestStatusShowsHomeChannel(t *testing.T) {
	rt := &fakeRuntime{status: gateway.RuntimeStatus{Version: "1.0", Home: "telegram:c1/9"}}
	c := gateway.NewCommands(&gateway.CommandOptions{Runtime: rt})
	res := c.Dispatch(context.Background(), testTicket(gateway.RoleOwner), "/status")
	if !strings.Contains(res.Reply, "home telegram:c1/9") {
		t.Fatalf("/status = %+v, want home", res)
	}
}

// TestTimestampsAndDelayToggles: /timestamps and /delay write per-chat flags,
// report status, reject bad args, and stay owner-only.
func TestTimestampsAndDelayToggles(t *testing.T) {
	fs := &fakeSettings{chat: map[string]string{}}
	c := gateway.NewCommands(&gateway.CommandOptions{Settings: fs})
	owner := testTicket(gateway.RoleOwner)
	for _, tc := range []struct{ cmd, key, on, off string }{
		{"/timestamps", "timestamps", "timestamps on", "timestamps off"},
		{"/delay", "human_delay", "delay on", "delay off"},
	} {
		if res := c.Dispatch(context.Background(), owner, tc.cmd); !strings.Contains(res.Reply, "off") {
			t.Fatalf("%s default = %+v", tc.cmd, res)
		}
		if res := c.Dispatch(context.Background(), owner, tc.cmd+" on"); !strings.Contains(res.Reply, tc.on) {
			t.Fatalf("%s on = %+v", tc.cmd, res)
		}
		if got := fs.chat["telegram:c1."+tc.key]; got != "true" {
			t.Fatalf("%s stored = %q", tc.key, got)
		}
		if res := c.Dispatch(context.Background(), owner, tc.cmd+" status"); !strings.Contains(res.Reply, tc.on) {
			t.Fatalf("%s status = %+v", tc.cmd, res)
		}
		if res := c.Dispatch(context.Background(), owner, tc.cmd+" off"); !strings.Contains(res.Reply, tc.off) {
			t.Fatalf("%s off = %+v", tc.cmd, res)
		}
		if res := c.Dispatch(context.Background(), owner, tc.cmd+" explode"); !strings.Contains(res.Reply, "usage:") {
			t.Fatalf("%s bad arg = %+v", tc.cmd, res)
		}
		if res := c.Dispatch(context.Background(), testTicket(gateway.RoleUser), tc.cmd+" on"); !strings.Contains(res.Reply, "owner-only") {
			t.Fatalf("%s as user = %+v", tc.cmd, res)
		}
	}
}
