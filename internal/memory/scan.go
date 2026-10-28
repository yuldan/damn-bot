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

// Finding is one threat-scan observation; Detail never contains a secret.
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

// ScanText classifies text for the quarantine gate (SPEC §14.4); a redactor
// change means a secret (high), and nil is "no redactor".
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
	if p := firstMatch(lower, "load_dotenv", "dotenv.config", "dotenv/config", "require('dotenv')", `require("dotenv")`, "cat .env", "source .env", "grep .env", "read .env", "parse .env", "cp .env"); p != "" {
		s.add("env_file", SeverityMedium, p)
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

// firstInjection matches the prompt-injection patterns of SPEC §14.4.
func firstInjection(lower string) string {
	return firstMatch(lower, "ignore all instructions", "ignore previous instructions",
		"ignore the previous instructions", "you are now", "system prompt", "<system>", "</system>")
}

func firstMatch(s string, patterns ...string) string {
	for _, p := range patterns {
		if strings.Contains(s, p) {
			return p
		}
	}
	return ""
}

// firstDestructive is an advisory subset; the authoritative classifier is
// internal/exec/classify. Medium, so it never auto-rejects a document.
func firstDestructive(lower string) string {
	return firstMatch(lower, "rm -rf /", "mkfs.", ":(){", "> /dev/sd")
}

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
