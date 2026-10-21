package provider

import (
	"errors"
	"regexp"
	"strconv"

	"github.com/damnbot/damnbot/internal/netx/retry"
)

// ContextWindow extracts a model's context window from an error the retry
// layer classifies as context_length (GAP-COST-02/PROV-02): the window is
// learned from a real request error, never from a probe. Only explicit
// "maximum context length" phrasing is accepted; an unrecognized message is
// not guessed (the caller keeps the previous window).
func ContextWindow(err error) (int64, bool) {
	if retry.ClassifyErr(err) != retry.ContextLength {
		return 0, false
	}
	e, ok := errors.AsType[*Error](err)
	if !ok {
		return 0, false
	}
	msg := string(e.Body)
	if msg == "" && e.Err != nil {
		msg = e.Err.Error()
	}
	for _, re := range [...]string{
		`maximum context length is (\d+)`,
		`maximum context length: (\d+)`,
		`context length of (\d+)`,
		`context window (?:is|of) (\d+)`,
		`\d+\s+tokens?\s*>\s*(\d+)`,
	} {
		m := regexp.MustCompile(`(?i)` + re).FindStringSubmatch(msg)
		if m == nil {
			continue
		}
		n, perr := strconv.ParseInt(m[1], 10, 64)
		if perr == nil && n > 0 {
			return n, true
		}
	}
	return 0, false
}
