package code

import (
	"errors"
	"testing"
)

func TestDecideMatrix(t *testing.T) {
	t.Parallel()
	base := PolicyInput{Mode: ModeAuto, AutoLanguages: []string{"python"}, WorkspaceTrusted: true}
	cases := []struct {
		name string
		lang Language
		in   PolicyInput
		want Requirement
	}{
		{"ask always asks", Python, PolicyInput{Mode: ModeAsk, AutoLanguages: []string{"python"}, WorkspaceTrusted: true}, RequireAsk},
		{"auto-edit always asks", Python, PolicyInput{Mode: ModeAutoEdit, AutoLanguages: []string{"python"}, WorkspaceTrusted: true}, RequireAsk},
		{"auto allowed trusted", Python, base, RequireAuto},
		{"auto language not allowed", JavaScript, base, RequireAsk},
		{"auto untrusted", Python, PolicyInput{Mode: ModeAuto, AutoLanguages: []string{"python"}}, RequireAsk},
		{"auto tainted", Python, PolicyInput{Mode: ModeAuto, AutoLanguages: []string{"python"}, WorkspaceTrusted: true, Tainted: true}, RequireAsk},
		{"auto wrote without approval", Python, PolicyInput{Mode: ModeAuto, AutoLanguages: []string{"python"}, WorkspaceTrusted: true, WroteWithoutApproval: true}, RequireAsk},
		{"unknown mode denies", Python, PolicyInput{Mode: "yolo", AutoLanguages: []string{"python"}}, RequireDeny},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d, err := Decide(tc.lang, tc.in)
			if err != nil || d.Requirement != tc.want {
				t.Fatalf("Decide = %+v, %v; want %s", d, err, tc.want)
			}
		})
	}
	if _, err := Decide("rust", base); !errors.Is(err, ErrLanguage) {
		t.Fatalf("unsupported = %v", err)
	}
}

func TestDecideHeadlessDeniesWithoutAuto(t *testing.T) {
	t.Parallel()
	ok := PolicyInput{Mode: ModeAuto, AutoLanguages: []string{"go"}, WorkspaceTrusted: true}
	if d, err := DecideHeadless(Go, ok); err != nil || d.Requirement != RequireAuto {
		t.Fatalf("auto headless = %+v, %v", d, err)
	}
	if d, err := DecideHeadless(Go, PolicyInput{Mode: ModeAsk, AutoLanguages: []string{"go"}}); err != nil || d.Requirement != RequireDeny {
		t.Fatalf("ask headless = %+v, %v", d, err)
	}
	if d, err := DecideHeadless(Go, PolicyInput{Mode: ModeAuto, AutoLanguages: []string{"go"}, WorkspaceTrusted: true, Tainted: true}); err != nil || d.Requirement != RequireDeny {
		t.Fatalf("tainted headless = %+v, %v", d, err)
	}
}

func TestAuthorizeDenyDoesNotSpawn(t *testing.T) {
	t.Parallel()
	b := newFake()
	r := mustRunner(t, b, nil)
	d, err := DecideHeadless(Python, PolicyInput{Mode: ModeAsk})
	if err != nil {
		t.Fatalf("DecideHeadless: %v", err)
	}
	if err := Authorize(d); !errors.Is(err, ErrDenied) {
		t.Fatalf("Authorize = %v", err)
	}
	if b.count() != 0 {
		t.Fatalf("spawned after deny: %d", b.count())
	}
	_ = r
}

func TestPreviewOmitsCodeBody(t *testing.T) {
	t.Parallel()
	got := Preview(&Request{Language: Python, Code: "print('secret-value')"})
	if contains(got, "secret-value") || contains(got, "language: python") == false {
		t.Fatalf("preview = %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
