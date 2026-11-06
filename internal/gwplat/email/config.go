package email

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"time"
)

// Config is the explicit activation of the adapter; the zero value is dormant.
type Config struct {
	Enabled  bool
	Address  string
	IMAPAddr string
	SMTPAddr string
	Mailbox  string
	TLS      *tls.Config
	Dial     func(ctx context.Context, network, addr string) (net.Conn, error)
	Interval time.Duration
	IDLE     time.Duration
	Timeout  time.Duration
	Cursors  CursorStore
	Wait     func(ctx context.Context, d time.Duration) error
	Log      *slog.Logger
	Now      func() time.Time
}
