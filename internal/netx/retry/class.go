// Package retry is the single retry policy of Damn-Bot (SPEC P6, §4.1.1,
// ENGINEERING E-32): typed error classes, the backoff loop, and the provider
// circuit breaker. Providers, platform adapters, and module backends classify
// failures into a [Class] and retry only through [Do]; no other package
// implements backoff.
package retry

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"
)

// Class is the closed failure class set of SPEC §4.1.1 plus [Unclassified]
// (ADR-100: errors no rule recognises are never retried).
type Class uint8

// Failure classes (SPEC §4.1.1). The zero value [None] means success.
const (
	None Class = iota
	RateLimited
	Overloaded
	Server
	Timeout
	Network
	StreamBroken
	ContextLength
	EmptyResponse
	Auth
	BadRequest
	OfflineMode
	Cancelled
	Unclassified
)

// String returns the snake_case wire name used in events and logs.
func (c Class) String() string {
	switch c {
	case None:
		return "none"
	case RateLimited:
		return "rate_limited"
	case Overloaded:
		return "overloaded"
	case Server:
		return "server"
	case Timeout:
		return "timeout"
	case Network:
		return "network"
	case StreamBroken:
		return "stream_broken"
	case ContextLength:
		return "context_length"
	case EmptyResponse:
		return "empty_response"
	case Auth:
		return "auth"
	case BadRequest:
		return "bad_request"
	case OfflineMode:
		return "offline_mode"
	case Cancelled:
		return "cancelled"
	case Unclassified:
		return "unclassified"
	}
	return "unclassified"
}

// Retryable reports whether [Do] retries the class on the same model, and
// whether model fallback may follow once attempts are exhausted (SPEC
// §4.1.1). empty_response and context_length have their own single re-call
// owned by the loop (ADR-078 AUD-045), so they are not retryable here.
func (c Class) Retryable() bool {
	// RateLimited..StreamBroken are contiguous: rate_limited, overloaded,
	// server, timeout, network, stream_broken.
	return c >= RateLimited && c <= StreamBroken
}

// Classer is implemented by errors that already know their class, such as
// netx transport errors and provider.Error. It takes precedence in [ClassifyErr].
type Classer interface {
	RetryClass() Class
}

// ServerHint is implemented by errors carrying a server Retry-After hint.
type ServerHint interface {
	RetryAfter() time.Duration
}

// ContextLengthCodes are the error.code values that turn a 400/413 into
// [ContextLength] (SPEC §4.1.1).
const (
	CodeContextLengthExceeded = "context_length_exceeded"
	CodeStringAboveMaxLength  = "string_above_max_length"
)

// ClassifyStatus maps an HTTP status and the provider error.code to a class.
// 429 is rate_limited whatever the body says (EC-PROV-029: rate limit wins
// over auth wording). Statuses outside the SPEC table are bad_request
// (non-retryable, ADR-100).
func ClassifyStatus(status int, code string) Class {
	switch {
	case status < http.StatusBadRequest:
		return None
	case status == http.StatusTooManyRequests:
		return RateLimited
	case status == http.StatusServiceUnavailable || status == 529:
		return Overloaded
	case status == http.StatusInternalServerError || status == http.StatusBadGateway ||
		status == http.StatusGatewayTimeout:
		return Server
	case status == http.StatusRequestTimeout:
		return Timeout
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return Auth
	case (status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge) &&
		(code == CodeContextLengthExceeded || code == CodeStringAboveMaxLength):
		return ContextLength
	default:
		return BadRequest
	}
}

// ClassifyErr maps a Go error to a class through errors.Is/As only (E-21).
// A deadline (context.DeadlineExceeded, net timeouts) is a timeout; a user
// cancel is cancelled.
func ClassifyErr(err error) Class {
	var (
		cl Classer
		ne net.Error
		oe *net.OpError
	)
	switch {
	case err == nil:
		return None
	case errors.As(err, &cl):
		return cl.RetryClass()
	case errors.Is(err, ErrCircuitOpen):
		return Overloaded
	case errors.Is(err, context.Canceled):
		return Cancelled
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return Timeout
	case errors.As(err, &oe), errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, syscall.ECONNRESET):
		return Network
	default:
		return Unclassified
	}
}
