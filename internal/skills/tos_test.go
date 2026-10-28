package skills

import (
	"errors"
	"strings"
	"testing"
)

// TestClassifyToS locks the two GAP-GW-07 pattern families and the false
// negatives that must stay none.
func TestClassifyToS(t *testing.T) {
	t.Parallel()
	high := []string{
		"godmode",
		"Persistent jailbreak across every turn",
		"stay in DAN mode",
		"do anything now",
		"rotate subscriptions between provider accounts",
		"account-pool rotation",
		"multi-account helper",
		"credential pool for ten subscriptions",
	}
	for _, text := range high {
		m := ClassifyToS(text)
		if m.Risk != ToSHigh || !strings.Contains(m.Note, "pattern") {
			t.Errorf("ClassifyToS(%q) = %+v, want high with a note", text, m)
		}
	}
	for _, text := range []string{"rotate the image", "account for errors", "security review"} {
		m := ClassifyToS(text)
		if m.Risk != ToSNone || m.Note == "" {
			t.Errorf("ClassifyToS(%q) = %+v, want none with a note", text, m)
		}
	}
}

// TestToSMetaRequired: both fields are mandatory at decode.
func TestToSMetaRequired(t *testing.T) {
	t.Parallel()
	good := `{"tos_risk":"high","tos_note":"jailbreak pattern","extra":1}`
	m, err := ParseToSMeta([]byte(good))
	if err != nil || m.Risk != ToSHigh || m.Note != "jailbreak pattern" {
		t.Fatalf("ParseToSMeta = %+v, %v", m, err)
	}
	for _, bad := range []string{
		`{"tos_note":"x"}`,
		`{"tos_risk":"high"}`,
		`{"tos_risk":"","tos_note":"x"}`,
		`{"tos_risk":"unofficial","tos_note":"x"}`,
		`{"tos_risk":"low","tos_note":"  "}`,
		`{"tos_risk":1,"tos_note":"x"}`,
		`not json`,
	} {
		if _, err := ParseToSMeta([]byte(bad)); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseToSMeta(%s) err = %v, want ErrInvalid", bad, err)
		}
	}
}

// TestRequireToSAck: high needs the entry name typed; lower levels do not.
func TestRequireToSAck(t *testing.T) {
	t.Parallel()
	high := ToSMeta{Risk: ToSHigh, Note: "jailbreak"}
	if err := RequireToSAck(high, "godmode", " godmode\n"); err != nil {
		t.Errorf("exact ack = %v", err)
	}
	for _, typed := range []string{"", "yes", "god mode", "godmode2"} {
		if err := RequireToSAck(high, "godmode", typed); !errors.Is(err, ErrToSAck) {
			t.Errorf("typed %q err = %v, want ErrToSAck", typed, err)
		}
	}
	if err := RequireToSAck(ToSMeta{Risk: ToSLow, Note: "unofficial"}, "tool", ""); err != nil {
		t.Errorf("low ack = %v", err)
	}
	if err := RequireToSAck(ToSMeta{Risk: "bogus", Note: "x"}, "x", ""); !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid meta err = %v, want ErrInvalid", err)
	}
}

// TestHubCorpusToSLabels mirrors the hub acceptance: every corpus entry that
// names a jailbreak or multi-account rotation carries tos_risk, and every
// entry (including clean ones) carries a note.
func TestHubCorpusToSLabels(t *testing.T) {
	t.Parallel()
	corpus := []string{
		"godmode: persistent jailbreak with load_dotenv",
		"account-pool: rotate subscriptions between Anthropic and Codex",
		"issue triage helper",
		"markdown formatter",
	}
	for _, entry := range corpus {
		m := ClassifyToS(entry)
		if err := m.Validate(); err != nil {
			t.Errorf("ClassifyToS(%q) = %+v: %v", entry, m, err)
		}
		keyword := strings.Contains(strings.ToLower(entry), "jailbreak") ||
			strings.Contains(strings.ToLower(entry), "account-pool") ||
			strings.Contains(strings.ToLower(entry), "rotate subscriptions")
		if keyword && m.Risk != ToSHigh {
			t.Errorf("%q has a ToS keyword but risk %q", entry, m.Risk)
		}
	}
}
