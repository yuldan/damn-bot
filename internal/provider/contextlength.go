package provider

import (
	"errors"
	"regexp"
	"strconv"

	"github.com/damnbot/damnbot/internal/netx/retry"
)

// contextWindowPatterns are the only accepted shapes of the structured
// error.message; free text and echoed prompt content are never parsed.
var contextWindowPatterns = []*regexp.Regexp{ //nolint:gochecknoglobals // fixed patterns (E-10)
	regexp.MustCompile(`(?i)maximum context length\s*(?:is|of|:)\s*(\d+)`),
}

// ContextWindow extracts a model's context window from the structured
// error.message of a context_length error (GAP-COST-02/PROV-02): the window
// is learned from a real request error, never from a probe. Only the
// "maximum context length" phrasing is accepted; an unrecognized message is
// not guessed (the caller keeps the previous window).
func ContextWindow(err error) (int64, bool) {
	if retry.ClassifyErr(err) != retry.ContextLength {
		return 0, false
	}
	e, ok := errors.AsType[*Error](err)
	if !ok || e.Message == "" {
		return 0, false
	}
	for _, re := range contextWindowPatterns {
		m := re.FindStringSubmatch(e.Message)
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
