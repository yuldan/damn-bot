package memory

import (
	"encoding/json"
	"strings"

	"github.com/damnbot/damnbot/internal/redact"
)

// Severity is the threat-scan level of SPEC §14.4. Only high auto-rejects.
type Severity string

// Scan severities.
const (
	SeverityLow    Severity = "low"
	SeverityMedium Severity = "medium"
	SeverityHigh   Severity = "high"
)

// Finding is one threat-scan observation. Detail never contains a secret:
// matched token values are omitted so the ledger cannot leak what was
// redacted.
type Finding struct {
	Category string   `json:"category"`
	Severity Severity `json:"severity"`
	Detail   string   `json:"detail,omitempty"`
}

// Scan is the stored scan_result JSON of one entry.
type Scan struct {
	Severity Severity  `json:"severity"`
	Findings []Finding `json:"findings,omitempty"`
}

// JSON renders the scan for memory.scan_result (valid JSON by construction).
func (s Scan) JSON() (string, error) {
	b, err := json.Marshal(s)
	return string(b), err
}

// High reports whether the scan must auto-reject (SPEC §14.4).
func (s Scan) High() bool { return s.Severity == SeverityHigh }

// ScanText classifies text for the quarantine gate (SPEC §14.4). The
// redactor is the same one used on write; when it changes the text the entry
// contains a secret and is high severity. nil is treated as "no redactor":
// callers that accept untrusted content must pass one.
func ScanText(red *redact.Redactor, text string) Scan {
	s := Scan{Severity: SeverityLow}
	lower := strings.ToLower(text)
	if p := firstInjection(lower); p != "" {
		s.add("prompt_injection", SeverityHigh, p)
	}
	if hasInvisible(text) {
		s.add("invisible", SeverityHigh, "invisible character")
	}
	if red != nil && red.String(text) != text {
		s.add("secret", SeverityHigh, "")
	}
	if p := firstDestructive(lower); p != "" {
		s.add("destructive", SeverityMedium, p)
	}
	if strings.Contains(lower, "http://") || strings.Contains(lower, "https://") {
		s.add("url", SeverityMedium, "non-allowlisted URL")
	}
	return s
}

func (s *Scan) add(category string, sev Severity, detail string) {
	s.Findings = append(s.Findings, Finding{Category: category, Severity: sev, Detail: detail})
	if rank(sev) > rank(s.Severity) {
		s.Severity = sev
	}
}

func rank(s Severity) int {
	switch s {
	case SeverityHigh:
		return 2
	case SeverityMedium:
		return 1
	case SeverityLow:
		return 0
	}
	return 0
}

// firstInjection matches the prompt-injection patterns of SPEC §14.4 on the
// already lower-cased text. A local slice keeps the package free of mutable
// globals (E-10).
func firstInjection(lower string) string {
	for _, p := range [...]string{
		"ignore all instructions",
		"ignore previous instructions",
		"ignore the previous instructions",
		"you are now",
		"system prompt",
		"<system>",
		"</system>",
	} {
		if strings.Contains(lower, p) {
			return p
		}
	}
	return ""
}

// firstDestructive is an advisory subset; the authoritative classifier is
// internal/exec/classify (F2 cannot import it). Medium, so it never
// auto-rejects a legitimate skill that merely documents a command.
func firstDestructive(lower string) string {
	for _, p := range [...]string{"rm -rf /", "mkfs.", ":(){", "> /dev/sd"} {
		if strings.Contains(lower, p) {
			return p
		}
	}
	return ""
}

// hasInvisible reports the invisible/bidi ranges of SPEC §14.4.
func hasInvisible(s string) bool {
	for _, r := range s {
		switch {
		case r >= 0x200B && r <= 0x200F,
			r >= 0x202A && r <= 0x202E,
			r >= 0x2066 && r <= 0x2069,
			r >= 0xE0000 && r <= 0xE007F:
			return true
		}
	}
	return false
}
