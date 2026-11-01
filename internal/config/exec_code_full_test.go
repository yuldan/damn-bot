//go:build !minimal

package config

import "testing"

// TestCodeAutoLanguagesValidation checks that exec.code_auto_languages is a
// closed set of non-shell languages without duplicates (ADR-089).
func TestCodeAutoLanguagesValidation(t *testing.T) {
	t.Parallel()
	cases := []struct {
		langs []string
		ok    bool
	}{
		{nil, true},
		{[]string{"python", "go"}, true},
		{[]string{"bash"}, false},
		{[]string{"python", "python"}, false},
		{[]string{"rust"}, false},
	}
	for _, tc := range cases {
		cfg := Default()
		cfg.Exec.CodeAutoLanguages = tc.langs
		err := cfg.Validate()
		if tc.ok && err != nil {
			t.Errorf("%v: %v", tc.langs, err)
		}
		if !tc.ok && err == nil {
			t.Errorf("%v: accepted", tc.langs)
		}
	}
}
