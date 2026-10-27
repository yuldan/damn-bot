package skills

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func parseReq(t *testing.T, requires string) Skill {
	t.Helper()
	doc := "---\nname: demo\ndescription: demo skill\n" + requires + "\n---\nbody\n"
	sk, err := Parse([]byte(doc), "/s/SKILL.md", SourceUser)
	if err != nil {
		t.Fatalf("Parse(%q): %v", requires, err)
	}
	return sk
}

// TestSkillsRequiresSchema: the closed host schema parses; the legacy skill
// list still parses; unknown keys, bad versions and empty objects fail.
func TestSkillsRequiresSchema(t *testing.T) {
	t.Parallel()
	sk := parseReq(t, "requires: {bins: [rg@14.1.0, jq], os: [linux, darwin], arch: [amd64], gpu: true}")
	if sk.Host == nil {
		t.Fatal("requires object was not parsed")
	}
	if len(sk.Host.Bins) != 2 || sk.Host.Bins[0].Name != "rg" || sk.Host.Bins[0].MinVersion != "14.1.0" ||
		sk.Host.Bins[1].Name != "jq" || sk.Host.Bins[1].MinVersion != "" {
		t.Fatalf("bins = %+v", sk.Host.Bins)
	}
	if !sk.Host.GPU || len(sk.Host.OS) != 2 || len(sk.Host.Arch) != 1 {
		t.Fatalf("host = %+v", sk.Host)
	}
	legacy := parseReq(t, "requires: [other-skill]")
	if legacy.Host != nil || len(legacy.Requires) != 1 || legacy.Requires[0] != "other-skill" {
		t.Fatalf("legacy requires = %+v / host %+v", legacy.Requires, legacy.Host)
	}
	none := parseReq(t, "requires: {gpu: false}")
	if none.Host == nil || none.Host.GPU {
		t.Fatalf("gpu:false host = %+v", none.Host)
	}
}

// TestSkillsRequiresRejects: the host schema is closed; a typo is an error,
// never a silently ignored requirement.
func TestSkillsRequiresRejects(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{
		"requires: {gpu: true, colour: blue}",
		"requires: {bins: [rg@1.x]}",
		"requires: {}",
		"requires: {os: [Linux]}",
		"requires: {bins: [../evil]}",
		"requires: {gpu: maybe}",
		"requires: {os: []}",
	} {
		doc := "---\nname: demo\ndescription: demo skill\n" + bad + "\n---\nbody\n"
		if _, err := Parse([]byte(doc), "/s/SKILL.md", SourceUser); !errors.Is(err, ErrFrontMatter) {
			t.Errorf("Parse(%q) error = %v, want ErrFrontMatter", bad, err)
		}
	}
}

// fakeHost injects deterministic host facts.
func fakeHost(bins map[string]string, versions map[string]string, gpu func() (bool, error)) Host {
	return Host{
		GOOS:   "linux",
		GOARCH: "amd64",
		LookPath: func(name string) (string, error) {
			if bins[name] != "" {
				return bins[name], nil
			}
			return "", errors.New("not found")
		},
		Version: func(name string) (string, error) {
			if v, ok := versions[name]; ok {
				return v, nil
			}
			return "", errors.New("unknown")
		},
		GPU: gpu,
	}
}

// TestSkillsUnavailableBins: missing binary, too-old version, and an
// unverifiable version are all unavailable.
func TestSkillsUnavailableBins(t *testing.T) {
	t.Parallel()
	h := fakeHost(map[string]string{"rg": "/usr/bin/rg"}, map[string]string{"rg": "14.0.9"}, nil)
	cases := []struct {
		req  string
		want string
	}{
		{"requires: {bins: [jq]}", "jq not found"},
		{"requires: {bins: [rg@14.1.0]}", "rg 14.0.9 < 14.1.0"},
		{"requires: {bins: [rg@15]}", "rg 14.0.9 < 15"},
	}
	for _, tc := range cases {
		sk := parseReq(t, tc.req)
		got := sk.Unavailable(h)
		if len(got) != 1 || !strings.Contains(got[0].String(), tc.want) {
			t.Errorf("%s: reasons = %v, want %q", tc.req, got, tc.want)
		}
	}
	// A version that cannot be verified never passes silently.
	noversion := parseReq(t, "requires: {bins: [rg@14.1.0]}")
	unverifiable := fakeHost(map[string]string{"rg": "/usr/bin/rg"}, nil, nil)
	if got := noversion.Unavailable(unverifiable); len(got) != 1 || !strings.Contains(got[0].String(), "version unknown") {
		t.Errorf("unverifiable version reasons = %v", got)
	}
}

// TestSkillsUnavailableOSArchGPU: os/arch mismatches and every GPU state are
// explicit; a missing detector is unavailable, never a pass.
func TestSkillsUnavailableOSArchGPU(t *testing.T) {
	t.Parallel()
	h := fakeHost(nil, nil, nil)
	sk := parseReq(t, "requires: {os: [darwin], arch: [arm64], gpu: true}")
	got := sk.Unavailable(h)
	want := []string{"os: linux not in [darwin]", "arch: amd64 not in [arm64]", "gpu: required but no detector is available"}
	if len(got) != len(want) {
		t.Fatalf("reasons = %v, want %v", got, want)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Errorf("reason[%d] = %q, want %q", i, got[i].String(), want[i])
		}
	}
	noGPU := fakeHost(nil, nil, func() (bool, error) { return false, nil })
	gpuSkill := parseReq(t, "requires: {gpu: true}")
	if got := gpuSkill.Unavailable(noGPU); len(got) != 1 || !strings.Contains(got[0].String(), "none detected") {
		t.Errorf("no GPU reasons = %v", got)
	}
	broken := fakeHost(nil, nil, func() (bool, error) { return false, errors.New("probe failed") })
	if got := gpuSkill.Unavailable(broken); len(got) != 1 || !strings.Contains(got[0].String(), "probe failed") {
		t.Errorf("broken detector reasons = %v", got)
	}
}

// TestSkillsAvailable: a satisfied requirement (and no requirement at all)
// yields no reason, so the catalog shows the skill.
func TestSkillsAvailable(t *testing.T) {
	t.Parallel()
	h := fakeHost(map[string]string{"rg": "/usr/bin/rg"}, map[string]string{"rg": "14.1.0"},
		func() (bool, error) { return true, nil })
	for _, req := range []string{
		"requires: {bins: [rg@14.1.0], os: [linux], arch: [amd64], gpu: true}",
		"requires: {bins: [rg]}",
		"",
	} {
		sk := parseReq(t, req)
		if got := sk.Unavailable(h); len(got) != 0 {
			t.Errorf("%q: reasons = %v, want none", req, got)
		}
	}
}

// TestSkillsRequiresJSON: the stored index representation round-trips and is
// strict (unknown fields, malformed JSON are errors; "" = no requirement).
func TestSkillsRequiresJSON(t *testing.T) {
	t.Parallel()
	h := &HostRequirement{
		Bins: []BinRequirement{{Name: "rg", MinVersion: "14.1.0"}, {Name: "jq"}},
		OS:   []string{"linux", "darwin"}, Arch: []string{"amd64"}, GPU: true,
	}
	enc, err := h.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	got, err := ParseHostRequirementJSON(enc)
	if err != nil {
		t.Fatalf("ParseHostRequirementJSON(%q): %v", enc, err)
	}
	if !slices.Equal(got.OS, h.OS) || !slices.Equal(got.Arch, h.Arch) || got.GPU != h.GPU ||
		len(got.Bins) != 2 || got.Bins[0].Name != "rg" || got.Bins[0].MinVersion != "14.1.0" {
		t.Fatalf("round-trip = %+v", got)
	}
	none, err := (*HostRequirement)(nil).JSON()
	if err != nil || none != "" {
		t.Fatalf("nil JSON = (%q, %v)", none, err)
	}
	if got, err := ParseHostRequirementJSON(""); got != nil || err != nil {
		t.Fatalf("empty parse = (%v, %v)", got, err)
	}
	for _, bad := range []string{"{", `{"gpu":"yes"}`, `{"colour":true}`} {
		if _, err := ParseHostRequirementJSON(bad); err == nil {
			t.Errorf("ParseHostRequirementJSON(%q) = nil error", bad)
		}
	}
}
