package e2e

// Live Bot API harness for the F4 Telegram credential. It talks to the real
// api.telegram.org with the token from ~/.config/damnbot/telegram.env, so it
// only runs when DAMNBOT_TELEGRAM_LIVE=1; the default suite never touches the
// network or the owner's chat.
//
// getUpdates is deliberately never called: another consumer may own the bot's
// update queue, and polling would steal those updates. Secrecy rules: the
// token, the request URL, and raw transport errors (which embed the URL) are
// never logged; only ok/username/message_id are.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
)

const (
	telegramLiveGate    = "DAMNBOT_TELEGRAM_LIVE"
	telegramEnvName     = "telegram.env"
	telegramAPIRoot     = "https://api.telegram.org/bot"
	telegramCallTimeout = 20 * time.Second
)

// telegramCreds is the three-line credential file contract.
type telegramCreds struct {
	token    string
	ownerID  string
	username string
}

// loadTelegramCreds parses <UserConfigDir>/damnbot/telegram.env. Error messages
// only ever name the path, never the values.
func loadTelegramCreds(t *testing.T) telegramCreds {
	t.Helper()
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatalf("user config dir: %v", err)
	}
	path := filepath.Join(dir, "damnbot", telegramEnvName)
	raw, err := os.ReadFile(path) // #nosec G304 -- path is <UserConfigDir>/damnbot, never user input
	if err != nil {
		t.Fatalf("read telegram credential file: %v", err)
	}
	creds := telegramCreds{}
	for line := range strings.SplitSeq(string(raw), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "DAMNBOT_TELEGRAM_TOKEN":
			creds.token = value
		case "DAMNBOT_TELEGRAM_OWNER_ID":
			creds.ownerID = value
		case "DAMNBOT_TELEGRAM_BOT_USERNAME":
			creds.username = value
		}
	}
	if creds.token == "" || creds.ownerID == "" || creds.username == "" {
		t.Fatal("telegram credential file must define token, owner id, and bot username")
	}
	return creds
}

// telegramBotNotStartedError classifies Bot API 400 "chat not found" for the
// owner chat: the bot has never been started, so it may not open the chat. The
// F4 gateway adapter can reuse errors.As + this wording instead of inventing a
// second message.
type telegramBotNotStartedError struct {
	username    string
	description string
}

func (e telegramBotNotStartedError) Error() string {
	return fmt.Sprintf("bot not started by owner (press Start in @%s): %s", e.username, e.description)
}

// httpDoer is the single method telegramAPI needs; netx.Client supplies it so
// E-05 (only netx constructs HTTP clients) holds for the live harness too.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// telegramAPI is a minimal Bot API client bound to one token.
type telegramAPI struct {
	token    string
	username string
	client   httpDoer
}

// redact removes the token from any text derived from a request or URL.
func (a telegramAPI) redact(s string) string {
	return strings.ReplaceAll(s, a.token, "[redacted]")
}

// call performs one Bot API method. form=nil means GET, otherwise a form POST.
func (a telegramAPI) call(ctx context.Context, method string, form url.Values, result any) error {
	endpoint := telegramAPIRoot + a.token + "/" + method
	var req *http.Request
	var err error
	if form == nil {
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	} else {
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	}
	if err != nil {
		return fmt.Errorf("telegram %s request: %s", method, a.redact(err.Error()))
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("telegram %s: %s", method, a.redact(err.Error()))
	}
	raw, readErr := io.ReadAll(resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil {
		return fmt.Errorf("telegram %s: read response: %w", method, readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("telegram %s: close response: %w", method, closeErr)
	}
	var envelope struct {
		OK          bool            `json:"ok"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}
	if uerr := json.Unmarshal(raw, &envelope); uerr != nil {
		return fmt.Errorf("telegram %s: decode response: %w", method, uerr)
	}
	if !envelope.OK {
		if envelope.ErrorCode == http.StatusBadRequest && strings.Contains(envelope.Description, "chat not found") {
			return telegramBotNotStartedError{username: a.username, description: envelope.Description}
		}
		return fmt.Errorf("telegram %s: ok=false code=%d description=%q", method, envelope.ErrorCode, envelope.Description)
	}
	if result == nil {
		return nil
	}
	if uerr := json.Unmarshal(envelope.Result, result); uerr != nil {
		return fmt.Errorf("telegram %s: decode result: %w", method, uerr)
	}
	return nil
}

// deleteMessage removes a probe message with a fresh context: t.Context is
// already cancelled when Cleanup callbacks run. It is only registered after a
// message_id exists.
func (a telegramAPI) deleteMessage(t *testing.T, ownerID string, messageID int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), telegramCallTimeout)
	defer cancel()
	form := url.Values{"chat_id": {ownerID}, "message_id": {fmt.Sprint(messageID)}}
	if err := a.call(ctx, "deleteMessage", form, nil); err != nil {
		t.Errorf("cleanup deleteMessage %d: %v", messageID, err)
	}
}

// TestE2ETelegramLiveBotAPI proves the provisioned token/owner pair works end
// to end: getMe must name the expected bot, sendMessage should reach the owner
// once, and the probe is deleted. A 400 "chat not found" is not a credential
// failure — the owner simply has not pressed Start — so that case skips with
// the API description instead of failing.
func TestE2ETelegramLiveBotAPI(t *testing.T) {
	if os.Getenv(telegramLiveGate) != "1" {
		t.Skipf("set %s=1 to run the live Bot API harness", telegramLiveGate)
	}
	t.Parallel()
	creds := loadTelegramCreds(t)
	api := telegramAPI{token: creds.token, username: creds.username, client: netx.New(netx.Config{}).Client("telegram-live", netx.Tool)}
	ctx, cancel := context.WithTimeout(t.Context(), telegramCallTimeout)
	defer cancel()

	var me struct {
		ID       int64  `json:"id"`
		IsBot    bool   `json:"is_bot"`
		Username string `json:"username"`
	}
	if err := api.call(ctx, "getMe", nil, &me); err != nil {
		t.Fatalf("getMe: %v", err)
	}
	if !me.IsBot {
		t.Fatalf("getMe: id %d is not a bot account", me.ID)
	}
	if me.Username != creds.username {
		t.Fatalf("getMe: username = %q, want %q", me.Username, creds.username)
	}
	t.Logf("getMe ok username=%s id=%d", me.Username, me.ID)

	marker := fmt.Sprintf("damnbot F4 live smoke %d", time.Now().UTC().UnixNano())
	form := url.Values{
		"chat_id":              {creds.ownerID},
		"text":                 {marker},
		"disable_notification": {"true"},
	}
	var sent struct {
		MessageID int64 `json:"message_id"`
	}
	if err := api.call(ctx, "sendMessage", form, &sent); err != nil {
		if notStarted, ok := errors.AsType[telegramBotNotStartedError](err); ok {
			t.Skipf("live send skipped: sendMessage: %v", notStarted)
		}
		t.Fatalf("sendMessage: %v", err)
	}
	if sent.MessageID == 0 {
		t.Fatal("sendMessage: ok but no message_id")
	}
	t.Logf("sendMessage ok message_id=%d", sent.MessageID)
	t.Cleanup(func() { api.deleteMessage(t, creds.ownerID, sent.MessageID) })
}
