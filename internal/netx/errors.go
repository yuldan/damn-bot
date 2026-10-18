package netx

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/damnbot/damnbot/internal/netx/retry"
)

// Policy refusals, raised before any byte leaves the process (ADR-100 butir 6).
var (
	// ErrOffline: a non-loopback dial while network.offline is set.
	ErrOffline = errors.New("offline mode: non-loopback dial refused")
	// ErrNonLoopbackInTest: a non-loopback dial from a test binary (E-69).
	ErrNonLoopbackInTest = errors.New("test binary: non-loopback dial refused")
	// ErrEgressDisabled: background egress while network.background = false (GAP-OPS-02).
	ErrEgressDisabled = errors.New("egress_disabled: background network access is off")
	// ErrBlockedHost: tool egress to a loopback, private, link-local, CGNAT,
	// or otherwise non-public address (SPEC §14.4, anti SSRF).
	ErrBlockedHost = errors.New("blocked_host: non-public address")
)

// Stream watchdog expiries (GAP-REL-07); both are class timeout.
var (
	ErrTTFB = errors.New("stream: no event before the time-to-first-byte deadline")
	ErrIdle = errors.New("stream: no event within the idle timeout")
)

// ErrConnectionLost marks a transport failure after the connection was
// established (reset or EOF mid-request), as opposed to [ConnectError]
// (EC-PROV-030).
var ErrConnectionLost = errors.New("connection lost mid-request")

// classedError gives a sentinel-based error its retry class.
type classedError struct {
	err   error
	class retry.Class
}

func (e *classedError) Error() string           { return e.err.Error() }
func (e *classedError) Unwrap() error           { return e.err }
func (e *classedError) RetryClass() retry.Class { return e.class }

func refuse(err error) error {
	c := retry.OfflineMode
	if errors.Is(err, ErrBlockedHost) {
		c = retry.BadRequest
	}
	return &classedError{err: err, class: c}
}

// ConnectError is a failure to establish a connection (DNS, refused,
// unreachable, TLS-less dial timeout, or a policy refusal): nothing was sent.
type ConnectError struct {
	Addr string
	Err  error
}

func (e *ConnectError) Error() string { return "connect " + e.Addr + ": " + e.Err.Error() }

// Unwrap exposes the dial error.
func (e *ConnectError) Unwrap() error { return e.Err }

// RetryClass is the class of the underlying dial error; an unrecognised
// dial failure is network.
func (e *ConnectError) RetryClass() retry.Class {
	if c := retry.ClassifyErr(e.Err); c != retry.Unclassified {
		return c
	}
	return retry.Network
}

// StatusError is a non-2xx response; Body holds at most maxErrorBody bytes
// so the provider can read error.code (SPEC §4.1.1) and refine the class.
type StatusError struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// maxErrorBody bounds the error body kept from a failed response.
const maxErrorBody = 64 << 10

func (e *StatusError) Error() string { return "http status " + strconv.Itoa(e.StatusCode) }

// RetryClass classifies by status alone; 429 is rate_limited whatever the
// body says (EC-PROV-029).
func (e *StatusError) RetryClass() retry.Class { return retry.ClassifyStatus(e.StatusCode, "") }

// RetryAfter parses the Retry-After header (delta-seconds or HTTP-date);
// 0 when absent or invalid.
func (e *StatusError) RetryAfter() time.Duration { return parseRetryAfter(e.Header.Get("Retry-After")) }

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if s, err := strconv.ParseUint(v, 10, 32); err == nil {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(time.Until(t), 0)
	}
	return 0
}

// classify makes the phase of a transport error explicit (EC-PROV-030): a
// dial failure stays a *ConnectError; a network failure or a bare EOF
// after the connection was up is ErrConnectionLost (class network).
func classify(err error) error {
	if _, ok := errors.AsType[*ConnectError](err); ok {
		return err
	}
	if c := retry.ClassifyErr(err); c == retry.Network || c == retry.Unclassified && errors.Is(err, io.EOF) {
		return &classedError{err: fmt.Errorf("%w: %w", ErrConnectionLost, err), class: retry.Network}
	}
	return err
}
