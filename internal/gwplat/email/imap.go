package email

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// IMAP failures. Compared with errors.Is.
var (
	ErrIMAP      = errors.New("email: imap failure")
	ErrIMAPFetch = errors.New("email: imap fetch failed")
)

// entry is one untagged IMAP response: its header line plus the optional
// literal of a BODY.PEEK fetch.
type entry struct {
	text    string
	literal []byte
}

// client is the minimal IMAP4rev1 surface the adapter needs on one reused
// connection: login, select, UID FETCH, UID STORE and logout.
type client struct {
	conn        net.Conn
	r           *bufio.Reader
	w           *bufio.Writer
	tags        int
	timeout     time.Duration
	uidValidity uint32
	highModSeq  uint64
}

func dialIMAP(ctx context.Context, cfg *Config, password string) (*client, error) {
	dial := cfg.Dial
	if dial == nil {
		d := net.Dialer{Timeout: cfg.Timeout}
		dial = d.DialContext
	}
	conn, err := dial(ctx, "tcp", cfg.IMAPAddr)
	if err != nil {
		return nil, fmt.Errorf("email: imap dial: %w", err)
	}
	if cfg.TLS != nil {
		tc := tls.Client(conn, cfg.TLS)
		if herr := tc.HandshakeContext(ctx); herr != nil {
			if cerr := conn.Close(); cerr != nil {
				return nil, fmt.Errorf("email: imap tls close: %w", cerr)
			}
			return nil, fmt.Errorf("email: imap tls: %w", herr)
		}
		conn = tc
	}
	c := &client{conn: conn, r: bufio.NewReader(conn), w: bufio.NewWriter(conn), timeout: cfg.Timeout}
	greeting, err := c.readText()
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(greeting, "* OK") && !strings.HasPrefix(greeting, "* PREAUTH") {
		return nil, fmt.Errorf("%w: greeting %q", ErrIMAP, greeting)
	}
	if _, err := c.cmd(ctx, "LOGIN", quote(cfg.Address), quote(password)); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *client) setDeadline() error {
	if c.timeout <= 0 {
		return nil
	}
	if err := c.conn.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		return fmt.Errorf("%w: deadline: %w", ErrIMAP, err)
	}
	return nil
}

func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func (c *client) readText() (string, error) {
	if err := c.setDeadline(); err != nil {
		return "", err
	}
	line, err := c.r.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("%w: read: %w", ErrIMAP, err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// readEntry reads one untagged response, including an inline literal followed
// by its continuation (“{123}“ then “)“).
func (c *client) readEntry() (entry, error) {
	text, err := c.readText()
	if err != nil {
		return entry{}, err
	}
	e := entry{text: text}
	n, ok := literalLen(text)
	if !ok {
		return e, nil
	}
	if n < 0 || n > 1<<24 {
		return entry{}, fmt.Errorf("%w: literal size %d", ErrIMAP, n)
	}
	buf := make([]byte, n)
	if _, rerr := io.ReadFull(c.r, buf); rerr != nil {
		return entry{}, fmt.Errorf("%w: literal: %w", ErrIMAP, rerr)
	}
	rest, err := c.readText()
	if err != nil {
		return entry{}, err
	}
	e.literal = buf
	e.text += " " + strings.TrimSpace(rest)
	return e, nil
}

// literalLen returns N for a line ending in {N}.
func literalLen(line string) (int, bool) {
	if !strings.HasSuffix(line, "}") {
		return 0, false
	}
	open := strings.LastIndexByte(line, '{')
	if open < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(line[open+1 : len(line)-1])
	return n, err == nil
}

func (c *client) cmd(ctx context.Context, name string, args ...string) ([]entry, error) {
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %w", ErrIMAP, ctx.Err())
	default:
	}
	c.tags++
	tag := "a" + strconv.Itoa(c.tags)
	var b strings.Builder
	b.WriteString(tag)
	b.WriteString(" ")
	b.WriteString(name)
	for _, arg := range args {
		b.WriteString(" ")
		b.WriteString(arg)
	}
	if err := c.setDeadline(); err != nil {
		return nil, err
	}
	if _, err := c.w.WriteString(b.String() + "\r\n"); err != nil {
		return nil, fmt.Errorf("%w: write: %w", ErrIMAP, err)
	}
	if err := c.w.Flush(); err != nil {
		return nil, fmt.Errorf("%w: flush: %w", ErrIMAP, err)
	}
	var out []entry
	for {
		e, err := c.readEntry()
		if err != nil {
			return out, err
		}
		if strings.HasPrefix(e.text, tag+" ") {
			if !strings.Contains(e.text, " OK") {
				return out, fmt.Errorf("%w: %s", ErrIMAP, e.text)
			}
			return out, nil
		}
		out = append(out, e)
	}
}

func (c *client) selectMailbox(ctx context.Context, mailbox string) error {
	entries, err := c.cmd(ctx, "SELECT", quote(mailbox))
	if err != nil {
		return err
	}
	c.uidValidity, c.highModSeq = 0, 0
	for i := range entries {
		if v, ok := bracketValue(entries[i].text, "UIDVALIDITY"); ok {
			if n, err := strconv.ParseUint(v, 10, 32); err == nil {
				c.uidValidity = uint32(n)
			}
		}
		if v, ok := bracketValue(entries[i].text, "HIGHESTMODSEQ"); ok {
			if n, err := strconv.ParseUint(v, 10, 64); err == nil {
				c.highModSeq = n
			}
		}
	}
	if c.uidValidity == 0 {
		return fmt.Errorf("%w: SELECT returned no UIDVALIDITY", ErrIMAP)
	}
	return nil
}

func bracketValue(text, key string) (string, bool) {
	rest, ok := strings.CutPrefix(text, "")
	if !ok {
		return "", false
	}
	_, after, found := strings.Cut(rest, "["+key+" ")
	if !found {
		return "", false
	}
	value, _, found := strings.Cut(after, "]")
	return value, found
}

// searchNew lists UIDs above since via UID FETCH (no search index required).
func (c *client) searchNew(ctx context.Context, since uint32) ([]uint32, error) {
	if since == ^uint32(0) {
		return nil, nil
	}
	entries, err := c.cmd(ctx, "UID FETCH", fmt.Sprintf("%d:*", since+1), "(UID)")
	if err != nil {
		return nil, err
	}
	var out []uint32
	for i := range entries {
		if uid, ok := uidOf(entries[i].text); ok && uid > since {
			out = append(out, uid)
		}
	}
	return out, nil
}

func uidOf(text string) (uint32, bool) {
	_, after, found := strings.Cut(text, "UID ")
	if !found {
		return 0, false
	}
	fields := strings.Fields(after)
	if len(fields) == 0 {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimRight(fields[0], ")"), 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(n), true
}

// fetchBody reads one body with BODY.PEEK so a rejected message stays UNSEEN.
func (c *client) fetchBody(ctx context.Context, uid uint32) ([]byte, error) {
	entries, err := c.cmd(ctx, "UID FETCH", strconv.FormatUint(uint64(uid), 10), "(UID BODY.PEEK[])")
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].literal != nil {
			return entries[i].literal, nil
		}
	}
	return nil, fmt.Errorf("%w: uid %d has no body", ErrIMAPFetch, uid)
}

// markSeen stores \Seen for an admitted message only.
func (c *client) markSeen(ctx context.Context, uid uint32) error {
	_, err := c.cmd(ctx, "UID STORE", strconv.FormatUint(uint64(uid), 10), "+FLAGS", `(\Seen)`)
	return err
}

func (c *client) logout() error {
	if c.conn == nil {
		return nil
	}
	_, err := c.cmd(context.Background(), "LOGOUT")
	cerr := c.conn.Close()
	c.conn = nil
	if err != nil {
		return err
	}
	if cerr != nil {
		return fmt.Errorf("email: imap close: %w", cerr)
	}
	return nil
}
