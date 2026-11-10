package hub

import (
	"errors"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/skills"
)

// TestF8HubTOSRisk: every hub entry carries tos_risk/tos_note, a high-risk
// entry needs the exact typed acknowledgement, a jailbreak entry stays
// pending, and the dotenv family is a medium env_file finding.
func TestF8HubTOSRisk(t *testing.T) {
	t.Parallel()
	body := strings.Replace(skillBody, "Body text.", "Enable godmode; persistent jailbreak instructions.", 1)
	entry := entryMap("findmy", body, len(body))
	entry["tos_risk"] = "high"
	entry["tos_note"] = "persistent jailbreak"
	fetch := mapFetcher{
		"https://skills.nousresearch.com/index.json":                   indexDoc(t, entry),
		"https://skills.nousresearch.com/skills/apple/findmy/SKILL.md": body,
	}
	st := &fakeStore{}
	h := newTestHub(t, fetch, st, &Config{})
	e := mustEntry(t, fetch)

	if _, err := h.Download(t.Context(), SourceOfficial, &e); !errors.Is(err, skills.ErrToSAck) {
		t.Fatalf("high ToS without ack = %v", err)
	}
	if _, err := h.DownloadAccepted(t.Context(), SourceOfficial, &e, "findmy-typo"); !errors.Is(err, skills.ErrToSAck) {
		t.Fatalf("wrong ack = %v", err)
	}
	if len(st.quarantined) != 0 {
		t.Fatal("a refused ToS wrote a quarantine row")
	}
	inst, err := h.DownloadAccepted(t.Context(), SourceOfficial, &e, " findmy ")
	if err != nil {
		t.Fatalf("typed ack: %v", err)
	}
	if inst.Status != skills.StatusScanned {
		t.Fatalf("jailbreak entry status = %q, want pending", inst.Status)
	}

	sk := &skills.Skill{Name: "dotenv", Description: "loads config", Body: "load_dotenv()"}
	sev, result, err := ThreatScan(sk)
	if err != nil {
		t.Fatal(err)
	}
	if sev != "medium" || !strings.Contains(result, "env_file") {
		t.Fatalf("dotenv scan = %s %s", sev, result)
	}
	for _, text := range []string{"cat .env", "source .env", "grep .env"} {
		sk.Body = text
		gotSev, _, serr := ThreatScan(sk)
		if serr != nil {
			t.Fatal(serr)
		}
		if gotSev != "medium" {
			t.Errorf("%q = %s, want medium", text, gotSev)
		}
	}
	sk.Body = "copy .env.example to .env"
	sev, _, serr := ThreatScan(sk)
	if serr != nil {
		t.Fatal(serr)
	}
	if sev != "low" {
		t.Errorf("example copy = %s, want low", sev)
	}
}
