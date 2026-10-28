package skills

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ToSRisk is the terms-of-service risk of one skills-hub or MCP-hub index
// entry (SPEC §22.1 GAP-GW-07, ADR-033 Q43). It is metadata, not a threat
// finding: high-risk entries stay installable after a typed acknowledgement
// and are quarantined as pending, never bundled.
type ToSRisk string

// ToS risk levels. The empty value is invalid: every index entry must state
// one of these explicitly.
const (
	ToSNone ToSRisk = "none"
	ToSLow  ToSRisk = "low"
	ToSHigh ToSRisk = "high"
)

// Valid reports whether r is one of the three stored levels.
func (r ToSRisk) Valid() bool {
	switch r {
	case ToSNone, ToSLow, ToSHigh:
		return true
	}
	return false
}

// ToSMeta is the mandatory ToS metadata of every skills-hub and MCP-hub index
// entry. Both fields are required: an entry without a level or a note fails
// [ToSMeta.Validate] and [ParseToSMeta].
type ToSMeta struct {
	Risk ToSRisk `json:"tos_risk"`
	Note string  `json:"tos_note"`
}

// Validate rejects an unknown level and a missing note.
func (m ToSMeta) Validate() error {
	if !m.Risk.Valid() {
		return fmt.Errorf("%w: tos_risk %q is not none|low|high", ErrInvalid, m.Risk)
	}
	if strings.TrimSpace(m.Note) == "" {
		return fmt.Errorf("%w: tos_note is empty", ErrInvalid)
	}
	return nil
}

// ParseToSMeta decodes the two mandatory fields of one hub index entry. Both
// keys must be present; unknown keys are ignored so a newer index stays
// readable. The index loaders call this for every entry, which is what makes
// the field mandatory rather than optional documentation.
func ParseToSMeta(data []byte) (ToSMeta, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return ToSMeta{}, fmt.Errorf("%w: tos metadata: %w", ErrInvalid, err)
	}
	riskRaw, ok := raw["tos_risk"]
	if !ok {
		return ToSMeta{}, fmt.Errorf("%w: tos_risk is missing", ErrInvalid)
	}
	var risk string
	if err := json.Unmarshal(riskRaw, &risk); err != nil {
		return ToSMeta{}, fmt.Errorf("%w: tos_risk: %w", ErrInvalid, err)
	}
	noteRaw, ok := raw["tos_note"]
	if !ok {
		return ToSMeta{}, fmt.Errorf("%w: tos_note is missing", ErrInvalid)
	}
	var note string
	if err := json.Unmarshal(noteRaw, &note); err != nil {
		return ToSMeta{}, fmt.Errorf("%w: tos_note: %w", ErrInvalid, err)
	}
	m := ToSMeta{Risk: ToSRisk(risk), Note: note}
	if err := m.Validate(); err != nil {
		return ToSMeta{}, err
	}
	return m, nil
}

// ClassifyToS derives the ToS metadata of one catalog entry from its text.
// Only the two pattern families named by GAP-GW-07 raise the level to high:
// a persistent jailbreak, or rotating multiple accounts/subscriptions. The
// note always names the matched pattern, so the install screen can explain
// why the acknowledgement is required.
func ClassifyToS(text string) ToSMeta {
	lower := foldText(text)
	if p := containsAny(lower, tosPatterns()); p != "" {
		return ToSMeta{Risk: ToSHigh, Note: `jailbreak/multi-account pattern "` + p + `"`}
	}
	return ToSMeta{Risk: ToSNone, Note: "no persistent-jailbreak or multi-account pattern"}
}

// RequireToSAck enforces the typed acknowledgement: a high-risk entry is only
// usable when the user types the entry name exactly (surrounding whitespace
// ignored). Low and none need no acknowledgement.
func RequireToSAck(m ToSMeta, name, typed string) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if m.Risk != ToSHigh {
		return nil
	}
	if name == "" {
		return fmt.Errorf("%w: entry name is empty", ErrInvalid)
	}
	if strings.TrimSpace(typed) != name {
		return fmt.Errorf("%w: type %q to accept the ToS risk", ErrToSAck, name)
	}
	return nil
}

// tosPatterns is the closed pattern set of GAP-GW-07. Functions keep the
// package free of mutable globals (E-10); the order fixes the reported note.
func tosPatterns() []string {
	return []string{
		"godmode",
		"persistent jailbreak",
		"jailbreak",
		"do anything now",
		"dan mode",
		"account-pool",
		"account pool",
		"account rotation",
		"rotate accounts",
		"multi-account",
		"multi account",
		"multiple accounts",
		"subscription rotation",
		"rotate subscriptions",
		"credential pool",
		"account farm",
	}
}
