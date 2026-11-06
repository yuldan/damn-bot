package email

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrSMTP is a failed SMTP reply.
var ErrSMTP = errors.New("email: smtp failure")

// composeMessage builds a plain-text reply with threading headers.
func composeMessage(from, to, subject, inReplyTo, body string, now time.Time) []byte {
	if subject == "" {
		subject = "Re: your message"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%s@%s>\r\n",
		from, to, sanitize(subject), now.Format(time.RFC1123Z), randomID(), domain(from))
	if inReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: <%s>\r\nReferences: <%s>\r\n", sanitize(inReplyTo), sanitize(inReplyTo))
	}
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return []byte(b.String())
}

// sanitize strips CR/LF so a header cannot be injected.
func sanitize(value string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
}

func randomID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}

// sendSMTP delivers one message; TLS is implicit when cfg.TLS is set.
func sendSMTP(ctx context.Context, cfg *Config, password, to string, msg []byte) error {
	dial := cfg.Dial
	if dial == nil {
		return fmt.Errorf("%w: injected dialer is required (E-05)", ErrSMTP)
	}
	conn, err := dial(ctx, "tcp", cfg.SMTPAddr)
	if err != nil {
		return fmt.Errorf("email: smtp dial: %w", err)
	}
	if cfg.TLS != nil {
		tc := tls.Client(conn, cfg.TLS)
		if err := tc.HandshakeContext(ctx); err != nil {
			if cerr := conn.Close(); cerr != nil {
				return fmt.Errorf("email: smtp tls close: %w", cerr)
			}
			return fmt.Errorf("email: smtp tls: %w", err)
		}
		conn = tc
	}
	defer func() {
		if cerr := conn.Close(); cerr != nil {
			return
		}
	}()
	if cfg.Timeout > 0 {
		if derr := conn.SetDeadline(time.Now().Add(cfg.Timeout)); derr != nil {
			return fmt.Errorf("email: smtp deadline: %w", derr)
		}
	}
	s := &smtpSession{r: bufio.NewReader(conn), w: bufio.NewWriter(conn)}
	if err := s.expect("220"); err != nil {
		return err
	}
	if err := s.envelope(cfg, password, to); err != nil {
		return err
	}
	if err := s.write(dotStuff(msg) + ".\r\n"); err != nil {
		return err
	}
	if err := s.expect("250"); err != nil {
		return err
	}
	return s.command("QUIT", "221")
}

// envelope runs EHLO/AUTH/MAIL/RCPT/DATA up to the dot terminator.
func (s *smtpSession) envelope(cfg *Config, password, to string) error {
	if err := s.command("EHLO "+domain(cfg.Address), "250"); err != nil {
		return err
	}
	if password != "" {
		auth := base64.StdEncoding.EncodeToString([]byte("\x00" + cfg.Address + "\x00" + password))
		if err := s.command("AUTH PLAIN "+auth, "235"); err != nil {
			return err
		}
	}
	if err := s.command("MAIL FROM:<"+cfg.Address+">", "250"); err != nil {
		return err
	}
	if err := s.command("RCPT TO:<"+to+">", "250"); err != nil {
		return err
	}
	return s.command("DATA", "354")
}

// dotStuff escapes a leading dot (RFC 5321 §4.5.2) and uses CRLF.
func dotStuff(msg []byte) string {
	lines := strings.Split(strings.ReplaceAll(string(msg), "\r\n", "\n"), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, ".") {
			lines[i] = "." + line
		}
	}
	return strings.Join(lines, "\r\n") + "\r\n"
}

type smtpSession struct {
	r *bufio.Reader
	w *bufio.Writer
}

func (s *smtpSession) command(line, want string) error {
	if err := s.write(line + "\r\n"); err != nil {
		return err
	}
	return s.expect(want)
}

func (s *smtpSession) write(line string) error {
	if _, err := s.w.WriteString(line); err != nil {
		return fmt.Errorf("%w: write: %w", ErrSMTP, err)
	}
	if err := s.w.Flush(); err != nil {
		return fmt.Errorf("%w: flush: %w", ErrSMTP, err)
	}
	return nil
}

// expect reads a (possibly multi-line) reply and matches its code.
func (s *smtpSession) expect(want string) error {
	for {
		line, err := s.r.ReadString('\n')
		if err != nil {
			return fmt.Errorf("%w: read: %w", ErrSMTP, err)
		}
		if len(line) < 3 {
			return fmt.Errorf("%w: short reply %q", ErrSMTP, line)
		}
		if line[:3] != want {
			return fmt.Errorf("%w: reply %q, want %s", ErrSMTP, strings.TrimSpace(line), want)
		}
		if len(line) > 3 && line[3] == '-' {
			continue
		}
		return nil
	}
}
