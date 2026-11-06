package irc

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
)

// Config is the explicit activation of the IRC adapter. The zero value is
// disabled and dials nothing. PLAIN requires the SASL password from the secret
// seam; EXTERNAL requires a TLS client certificate and reads no password.
type Config struct {
	Enabled          bool
	Addr             string
	Nick             string
	Channels         []string
	SASLMechanism    string
	SASLAccount      string
	TLS              *tls.Config
	Dial             func(ctx context.Context, network, addr string) (net.Conn, error)
	HandshakeTimeout time.Duration
	Backoff          gwplat.Backoff
	Log              *slog.Logger
	Now              func() time.Time
}

// Adapter is the IRC adapter. Identity is the SASL account tag, never the
// nickname (SEC-10): a server without SASL+account-tag yields
// module_unavailable{irc_no_account_auth} and dispatches nothing.
type Adapter struct {
	cfg      Config
	desc     *gwplat.Descriptor
	out      *gwplat.Outbox
	handler  gwplat.Handler
	password string
	dedupe   *gwplat.Dedupe
	log      *slog.Logger
	now      func() time.Time

	mu   sync.Mutex
	conn *client
}

// Errors of the adapter. Compared with errors.Is.
var (
	ErrConfig      = errors.New("irc: invalid config")
	ErrNoConn      = errors.New("irc: not connected")
	ErrBadTarget   = errors.New("irc: invalid target")
	ErrLineTooLong = errors.New("irc: line too long")
)

// SASL mechanisms.
const (
	MechanismPlain    = "PLAIN"
	MechanismExternal = "EXTERNAL"
)

const maxLine, maxAuthChunk, defaultTimeout = 8192, 400, 15 * time.Second

// New validates explicitly enabled config and optional secrets, and returns a
// dormant adapter. It opens no connection.
func New(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, secrets gwplat.SecretSource, out *gwplat.Outbox, h gwplat.Handler) (*Adapter, error) {
	if desc == nil || desc.Name != "irc" {
		return nil, fmt.Errorf("%w: descriptor is not irc", ErrConfig)
	}
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil config", ErrConfig)
	}
	if err := gwplat.RequireEnabled(desc.Name, cfg.Enabled); err != nil {
		return nil, fmt.Errorf("irc: activation: %w", err)
	}
	if cfg.Addr == "" || cfg.Nick == "" || cfg.SASLAccount == "" {
		return nil, fmt.Errorf("%w: addr, nick and SASL account are required", ErrConfig)
	}
	if out == nil || h == nil {
		return nil, fmt.Errorf("%w: outbox and handler are required", ErrConfig)
	}
	password, err := resolveSASL(ctx, cfg, desc, secrets)
	if err != nil {
		return nil, err
	}
	logger := cfg.Log
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Adapter{
		cfg:      *cfg,
		desc:     desc,
		out:      out,
		handler:  h,
		password: password,
		dedupe:   gwplat.NewDedupe(4096, 30*time.Minute),
		log:      logger,
		now:      now,
	}, nil
}

// resolveSASL returns the PLAIN password from the secret seam, or empty for
// EXTERNAL after requiring a TLS client certificate.
func resolveSASL(ctx context.Context, cfg *Config, desc *gwplat.Descriptor, secrets gwplat.SecretSource) (string, error) {
	switch normalizeMechanism(cfg.SASLMechanism) {
	case MechanismExternal:
		if cfg.TLS == nil {
			return "", fmt.Errorf("irc: external sasl: %w", gwplat.Unavailablef("irc", "irc_external_without_tls"))
		}
		return "", nil
	case MechanismPlain:
		values, err := gwplat.ResolveSecrets(ctx, desc, secrets)
		if err != nil {
			return "", fmt.Errorf("irc: sasl secret: %w", err)
		}
		return values["irc_sasl_password"], nil
	default:
		return "", fmt.Errorf("%w: SASL mechanism %q", ErrConfig, cfg.SASLMechanism)
	}
}

// Run keeps one authenticated session alive; an account-auth failure is
// permanent and returned, a transport failure reconnects with capped backoff.
func (a *Adapter) Run(ctx context.Context) error {
	for attempt := 1; ; attempt++ {
		err := a.session(ctx)
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		if errors.Is(err, gateway.ErrIRCNoAccountAuth) {
			return err
		}
		a.log.WarnContext(ctx, "irc session ended", "attempt", attempt, "err", err)
		if waitErr := a.cfg.Backoff.Wait(ctx, attempt); waitErr != nil {
			return fmt.Errorf("irc: reconnect: %w", waitErr)
		}
	}
}

func (a *Adapter) session(ctx context.Context) error {
	conn, err := a.dial(ctx)
	if err != nil {
		return err
	}
	// A cancelled parent must unblock a socket read, not wait for the peer.
	stop := context.AfterFunc(ctx, func() { closeConn(conn) })
	defer stop()
	timeout := a.cfg.HandshakeTimeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		closeConn(conn)
		return fmt.Errorf("irc: deadline: %w", err)
	}
	c := &client{conn: conn, r: bufio.NewReaderSize(conn, maxLine)}
	defer closeClient(c)
	if err := a.handshake(c); err != nil {
		return err
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return fmt.Errorf("irc: clear deadline: %w", err)
	}
	a.setClient(c)
	defer a.setClient(nil)
	return a.readLoop(ctx, c)
}

func (a *Adapter) dial(ctx context.Context) (net.Conn, error) {
	var (
		conn net.Conn
		err  error
	)
	if a.cfg.Dial != nil {
		conn, err = a.cfg.Dial(ctx, "tcp", a.cfg.Addr)
	} else {
		var dialer net.Dialer
		conn, err = dialer.DialContext(ctx, "tcp", a.cfg.Addr)
	}
	if err != nil {
		return nil, fmt.Errorf("irc: dial: %w", err)
	}
	if a.cfg.TLS == nil {
		return conn, nil
	}
	tlsConn := tls.Client(conn, a.cfg.TLS)
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		closeConn(conn)
		return nil, fmt.Errorf("irc: tls handshake: %w", err)
	}
	return tlsConn, nil
}

func closeConn(conn net.Conn) {
	if err := conn.Close(); err != nil {
		return
	}
}

func closeClient(c *client) {
	if err := c.close(); err != nil {
		return
	}
}

func (a *Adapter) handshake(c *client) error {
	if err := c.send("CAP LS 302"); err != nil {
		return err
	}
	if err := c.send("NICK " + a.cfg.Nick); err != nil {
		return err
	}
	if err := c.send("USER " + a.cfg.Nick + " 0 * :damnbot"); err != nil {
		return err
	}
	if err := a.negotiate(c); err != nil {
		return err
	}
	if err := c.send("CAP END"); err != nil {
		return err
	}
	if _, err := c.until(func(m message) bool { return m.command == "001" }); err != nil {
		return err
	}
	return a.joinChannels(c)
}

// negotiate requires SASL and account-tag, requests the offered capabilities,
// and completes the SASL exchange. Anything else is module_unavailable.
func (a *Adapter) negotiate(c *client) error {
	caps, err := a.readCaps(c)
	if err != nil {
		return err
	}
	if !caps["sasl"] || !caps["account-tag"] {
		return authUnavailable()
	}
	request := []string{"sasl", "account-tag"}
	if caps["account-notify"] {
		request = append(request, "account-notify")
	}
	if err := c.send("CAP REQ :" + strings.Join(request, " ")); err != nil {
		return err
	}
	if err := a.readCapAck(c); err != nil {
		return err
	}
	return a.authenticate(c)
}

func (a *Adapter) joinChannels(c *client) error {
	for _, channel := range a.cfg.Channels {
		if !validTarget(channel) {
			return fmt.Errorf("%w: channel %q", ErrConfig, channel)
		}
		if err := c.send("JOIN " + channel); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) readCaps(c *client) (map[string]bool, error) {
	m, err := c.until(func(m message) bool { return m.command == "CAP" && hasParam(m, "LS") })
	if err != nil {
		return nil, err
	}
	caps := make(map[string]bool)
	for cap := range strings.FieldsSeq(lastParam(m)) {
		name, _, found := strings.Cut(cap, "=")
		if !found {
			name = cap
		}
		caps[name] = true
	}
	return caps, nil
}

func (a *Adapter) readCapAck(c *client) error {
	m, err := c.until(func(m message) bool { return m.command == "CAP" })
	if err != nil {
		return err
	}
	if hasParam(m, "NAK") {
		return authUnavailable()
	}
	return nil
}

func (a *Adapter) authenticate(c *client) error {
	if err := c.send("AUTHENTICATE " + normalizeMechanism(a.cfg.SASLMechanism)); err != nil {
		return err
	}
	if m, err := c.until(func(m message) bool {
		return (m.command == "AUTHENTICATE" && lastParam(m) == "+") || isAuthFailure(m.command)
	}); err != nil {
		return err
	} else if isAuthFailure(m.command) {
		return authUnavailable()
	}
	if normalizeMechanism(a.cfg.SASLMechanism) == MechanismPlain {
		if err := c.sendAuth(a.cfg.SASLAccount, a.password); err != nil {
			return err
		}
	} else if err := c.send("+"); err != nil {
		return err
	}
	m, err := c.until(func(m message) bool { return m.command == "903" || isAuthFailure(m.command) })
	if err != nil {
		return err
	}
	if m.command != "903" {
		return authUnavailable()
	}
	return nil
}

func normalizeMechanism(raw string) string {
	if strings.EqualFold(raw, MechanismExternal) {
		return MechanismExternal
	}
	return MechanismPlain
}

func isAuthFailure(command string) bool {
	switch command {
	case "904", "905", "906", "907":
		return true
	}
	return false
}

func authUnavailable() error {
	return fmt.Errorf("%w: %w", gateway.ErrIRCNoAccountAuth, gwplat.Unavailablef("irc", "irc_no_account_auth"))
}

func (a *Adapter) readLoop(ctx context.Context, c *client) error {
	for {
		m, err := c.readLine()
		if err != nil {
			return fmt.Errorf("irc: read: %w", err)
		}
		switch m.command {
		case "PING":
			if err := c.send("PONG :" + lastParam(m)); err != nil {
				return err
			}
		case "ERROR":
			return fmt.Errorf("irc: server error: %s", lastParam(m))
		case "PRIVMSG":
			if err := a.handlePrivmsg(ctx, m); err != nil {
				return err
			}
		}
	}
}

func (a *Adapter) handlePrivmsg(ctx context.Context, m message) error {
	if len(m.params) < 2 {
		return nil
	}
	account := m.tags["account"]
	auth := gateway.IRCAuth{SASL: true, AccountTag: true}
	if _, ok := auth.Identity(account); !ok || strings.EqualFold(account, a.cfg.SASLAccount) {
		return nil
	}
	target, text := m.params[0], m.params[1]
	updateID := "irc:" + m.tags["msgid"]
	if m.tags["msgid"] == "" {
		sum := sha256.Sum256([]byte(m.nick + "\x00" + target + "\x00" + text))
		updateID = "irc:sha256:" + hex.EncodeToString(sum[:16])
	}
	if !a.dedupe.Claim(updateID, a.now()) {
		return nil
	}
	group := strings.HasPrefix(target, "#") || strings.HasPrefix(target, "&")
	chat := target
	if !group {
		chat = account
	}
	in := &gateway.Inbound{
		Platform:  "irc",
		Account:   a.cfg.SASLAccount,
		ChatID:    chat,
		SenderID:  account,
		UpdateID:  updateID,
		Text:      text,
		Group:     group,
		Mentioned: group && mentions(text, a.cfg.Nick),
	}
	if err := gwplat.Dispatch(ctx, a.handler, in); err != nil {
		return fmt.Errorf("irc: dispatch: %w", err)
	}
	return nil
}

func mentions(text, nick string) bool {
	for field := range strings.FieldsSeq(text) {
		field = strings.Trim(field, ":,;!?()[]{}<>\"'")
		if strings.EqualFold(field, nick) {
			return true
		}
	}
	return false
}

// Publish sends one already-rendered line to a channel or account. Chat names
// without spaces/control characters only.
func (a *Adapter) Publish(ctx context.Context, chat, text string) error {
	if !validTarget(chat) {
		return fmt.Errorf("%w: %q", ErrBadTarget, chat)
	}
	a.mu.Lock()
	c := a.conn
	a.mu.Unlock()
	if c == nil {
		return ErrNoConn
	}
	line := singleLine(text)
	if limit := a.desc.Caps.MaxLen; len(line) > limit {
		line = line[:limit]
	}
	if err := c.send("PRIVMSG " + chat + " :" + line); err != nil {
		return err
	}
	return nil
}

func (a *Adapter) setClient(c *client) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.conn = c
}

func validTarget(target string) bool {
	if target == "" || len(target) > 64 {
		return false
	}
	return !strings.ContainsAny(target, " \x00\r\n,")
}

func singleLine(text string) string {
	text = strings.ReplaceAll(text, "\r", " ")
	return strings.ReplaceAll(text, "\n", " ")
}

func hasParam(m message, want string) bool {
	for _, param := range m.params {
		if strings.EqualFold(param, want) {
			return true
		}
	}
	return false
}

// client serializes writes because outq workers may publish concurrently with
// the handshake.
type client struct {
	conn net.Conn
	r    *bufio.Reader
	mu   sync.Mutex
	w    *bufio.Writer
}

func (c *client) send(line string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.w == nil {
		c.w = bufio.NewWriter(c.conn)
	}
	if _, err := c.w.WriteString(line + "\r\n"); err != nil {
		return fmt.Errorf("irc: write: %w", err)
	}
	if err := c.w.Flush(); err != nil {
		return fmt.Errorf("irc: flush: %w", err)
	}
	return nil
}

func (c *client) sendAuth(account, password string) error {
	payload := base64.StdEncoding.EncodeToString([]byte("\x00" + account + "\x00" + password))
	for len(payload) > maxAuthChunk {
		if err := c.send("AUTHENTICATE " + payload[:maxAuthChunk]); err != nil {
			return err
		}
		payload = payload[maxAuthChunk:]
	}
	if err := c.send("AUTHENTICATE " + payload); err != nil {
		return err
	}
	if len(payload) == maxAuthChunk {
		return c.send("AUTHENTICATE +")
	}
	return nil
}

func (c *client) readLine() (message, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return message{}, fmt.Errorf("irc: connection: %w", err)
	}
	if len(line) > maxLine {
		return message{}, ErrLineTooLong
	}
	m, ok := parseLine(line)
	if !ok {
		return message{command: "EMPTY"}, nil
	}
	return m, nil
}

func (c *client) until(pred func(message) bool) (message, error) {
	for {
		m, err := c.readLine()
		if err != nil {
			return message{}, err
		}
		if m.command == "PING" {
			if err := c.send("PONG :" + lastParam(m)); err != nil {
				return message{}, err
			}
			continue
		}
		if m.command == "ERROR" {
			return message{}, fmt.Errorf("irc: server error: %s", lastParam(m))
		}
		if pred(m) {
			return m, nil
		}
	}
}

func (c *client) close() error {
	if err := c.conn.Close(); err != nil {
		return fmt.Errorf("irc: close: %w", err)
	}
	return nil
}
