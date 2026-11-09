package connectors

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/netx"
)

// discordMaxMessages bounds one message page (Discord's own API cap).
const discordMaxMessages = 100

// discordHandler serves the user-facing Discord tool and its admin sibling.
// The admin tool needs the explicit `admin = true` opt-in in config; a
// channel or message id from the model is escaped and validated before it can
// become a path segment.
type discordHandler struct {
	doer *Doer
	cfg  DiscordSection
	sec  SecretSource
	red  *Redactor
	base string
}

// DiscordSection is [modules.connectors.discord].
type DiscordSection struct {
	Enabled     bool   `json:"enabled"`
	BotTokenEnv string `json:"bot_token_env"`
	BaseURL     string `json:"base_url"`
	Admin       bool   `json:"admin"`
}

func (m *Module) newDiscord() (Handler, error) {
	cfg, err := decodeSub[DiscordSection](m.cfg.Discord, "discord")
	if err != nil || !cfg.Enabled {
		return nil, err
	}
	if cfg.BotTokenEnv == "" {
		return nil, &Error{Code: codeInvalidArgs, Message: "discord.bot_token_env is required"}
	}
	doer, err := NewDoer(DoerOptions{
		Client: m.network.Client("discord", netx.Tool), Timeout: m.cfg.Timeout,
		Retries: m.cfg.Retries, MaxBytes: m.cfg.MaxBytes, Redactor: m.redactor,
		Now: m.deps.Now,
	})
	if err != nil {
		return nil, err
	}
	base := discordBase(cfg)
	return &discordHandler{
		doer: doer, cfg: cfg, sec: m.deps.Secrets,
		red: m.redactor, base: base,
	}, nil
}

func discordBase(d DiscordSection) string {
	if d.BaseURL != "" {
		return strings.TrimRight(d.BaseURL, "/")
	}
	return "https://discord.com/api/v10"
}

func (d *discordHandler) Tools() []string { return []string{"discord", "discord_admin"} }

type discordArgs struct {
	Action    string `json:"action"`
	GuildID   string `json:"guild_id"`
	ChannelID string `json:"channel_id"`
	MessageID string `json:"message_id"`
	Query     string `json:"query"`
	Content   string `json:"content"`
	Limit     int    `json:"limit"`
}

func (d *discordHandler) Invoke(ctx context.Context, tool string, raw json.RawMessage, _ host.Env) (host.Result, error) {
	var a discordArgs
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, err
	}
	tok, err := ResolveSecret(d.sec, d.cfg.BotTokenEnv)
	if err != nil {
		return host.Result{}, err
	}
	d.red.Add(tok)
	switch tool {
	case "discord":
		return d.user(ctx, tok, &a)
	case "discord_admin":
		return d.admin(ctx, tok, &a)
	}
	return host.Result{}, &Error{Code: codeModuleUnavailable, Message: "unknown discord tool " + tool}
}

func (d *discordHandler) user(ctx context.Context, tok string, a *discordArgs) (host.Result, error) {
	switch a.Action {
	case "messages":
		return d.messages(ctx, tok, a)
	case "members":
		return d.members(ctx, tok, a)
	case "thread":
		return d.thread(ctx, tok, a)
	case "send":
		return d.send(ctx, tok, a)
	}
	return host.Result{}, &Error{Code: codeInvalidArgs, Message: "action must be messages, members, thread or send"}
}

func (d *discordHandler) messages(ctx context.Context, tok string, a *discordArgs) (host.Result, error) {
	limit, err := discordLimit(a.Limit)
	if err != nil {
		return host.Result{}, err
	}
	channel, err := escapePath(a.ChannelID)
	if err != nil {
		return host.Result{}, err
	}
	return d.read(ctx, tok, "/channels/"+channel+"/messages?limit="+strconv.Itoa(limit))
}

func (d *discordHandler) members(ctx context.Context, tok string, a *discordArgs) (host.Result, error) {
	if a.Query == "" {
		return host.Result{}, &Error{Code: codeInvalidArgs, Message: "query is required for members"}
	}
	guild, err := escapePath(a.GuildID)
	if err != nil {
		return host.Result{}, err
	}
	query := url.Values{"query": {a.Query}, "limit": {"50"}}
	return d.read(ctx, tok, "/guilds/"+guild+"/members/search?"+query.Encode())
}

func (d *discordHandler) thread(ctx context.Context, tok string, a *discordArgs) (host.Result, error) {
	channel, err := escapePath(a.ChannelID)
	if err != nil {
		return host.Result{}, err
	}
	name := strings.TrimSpace(a.Query)
	if name == "" {
		return host.Result{}, &Error{Code: codeInvalidArgs, Message: "query (thread name) is required"}
	}
	body, err := json.Marshal(map[string]any{"name": name, "type": 11})
	if err != nil {
		return host.Result{}, &Error{Code: codeInternal, Message: "encode thread", err: err}
	}
	return d.write(ctx, tok, "/channels/"+channel+"/threads", body)
}

func (d *discordHandler) send(ctx context.Context, tok string, a *discordArgs) (host.Result, error) {
	channel, err := escapePath(a.ChannelID)
	if err != nil {
		return host.Result{}, err
	}
	if strings.TrimSpace(a.Content) == "" {
		return host.Result{}, &Error{Code: codeInvalidArgs, Message: "content must not be empty"}
	}
	body, err := json.Marshal(map[string]string{"content": a.Content})
	if err != nil {
		return host.Result{}, &Error{Code: codeInternal, Message: "encode message", err: err}
	}
	return d.write(ctx, tok, "/channels/"+channel+"/messages", body)
}

func (d *discordHandler) admin(ctx context.Context, tok string, a *discordArgs) (host.Result, error) {
	if !d.cfg.Admin {
		return host.Result{}, &Error{
			Code:    codeDenied,
			Message: "discord_admin is disabled", Hint: "set [modules.connectors.discord].admin = true",
		}
	}
	guild, err := escapePath(a.GuildID)
	if err != nil {
		return host.Result{}, err
	}
	switch a.Action {
	case "channels":
		return d.read(ctx, tok, "/guilds/"+guild+"/channels")
	case "roles":
		return d.read(ctx, tok, "/guilds/"+guild+"/roles")
	case "pin", "unpin":
		channel, err := escapePath(a.ChannelID)
		if err != nil {
			return host.Result{}, err
		}
		message, err := escapePath(a.MessageID)
		if err != nil {
			return host.Result{}, err
		}
		path := "/channels/" + channel + "/pins/" + message
		method := http.MethodPut
		if a.Action == "unpin" {
			method = http.MethodDelete
		}
		return d.write(ctx, tok, path, []byte(`{}`), method)
	}
	return host.Result{}, &Error{Code: codeInvalidArgs, Message: "admin action must be channels, roles, pin or unpin"}
}

// read executes an idempotent GET and wraps the external JSON.
func (d *discordHandler) read(ctx context.Context, tok, path string) (host.Result, error) {
	var out json.RawMessage
	if _, err := d.doer.JSON(ctx, &Request{
		Method: http.MethodGet, URL: joinURL(d.base, path),
		Header: discordAuth(tok), Accept: "application/json", Retry: true,
		MaxBody: DefaultMaxBytes,
	}, &out); err != nil {
		return host.Result{}, err
	}
	if len(out) == 0 {
		out = json.RawMessage(`{}`)
	}
	return host.Result{Content: string(out), UntrustedSource: "connector:discord"}, nil
}

// write executes one mutation once; Discord is not idempotent here.
func (d *discordHandler) write(ctx context.Context, tok, path string, body []byte, methods ...string) (host.Result, error) {
	method := http.MethodPost
	if len(methods) > 0 {
		method = methods[0]
	}
	header := discordAuth(tok)
	maps.Copy(header, headerJSON())
	var out json.RawMessage
	if _, err := d.doer.JSON(ctx, &Request{
		Method: method, URL: joinURL(d.base, path),
		Header: header, Body: body, Accept: "application/json",
	}, &out); err != nil {
		return host.Result{}, err
	}
	if len(out) == 0 {
		out = json.RawMessage(`{}`)
	}
	return host.Result{Content: string(out), UntrustedSource: "connector:discord"}, nil
}

func discordAuth(tok string) http.Header {
	return http.Header{"Authorization": {"Bot " + tok}}
}

func discordLimit(limit int) (int, error) {
	if limit < 0 || limit > discordMaxMessages {
		return 0, &Error{Code: codeInvalidArgs, Message: "limit must be 1..100"}
	}
	if limit == 0 {
		limit = 50
	}
	return limit, nil
}
