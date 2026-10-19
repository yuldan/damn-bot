package config

import (
	"errors"
	"io/fs"
	"testing"
)

// FuzzConfigTOML (ENGINEERING §5.2): arbitrary TOML must either produce a
// valid config or a typed *Error; the loader never panics.
func FuzzConfigTOML(f *testing.F) {
	for _, seed := range []string{
		"",
		"[provider]\nmodel = \"p/m\"\n",
		"[provider]\nmodel = [\n",
		"[loop]\nmax_iterations = 40\n",
		"[approval]\nshell_allow = [[\"go\", \"test\"]]\n",
		"[[providers.custom]]\nname = \"x\"\n",
		"[memory]\nentry_max_chars = 500\n",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data string) {
		if len(data) > MaxFileSize+1 {
			return
		}
		l, err := New(Deps{
			ReadFile: func(string) ([]byte, error) { return nil, fs.ErrNotExist },
			Getenv:   func(string) string { return "" },
			HomeDir:  func() (string, error) { return "/home/u", nil },
			GOOS:     "linux",
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		cfg, err := l.Parse([]byte(data), "fuzz.toml")
		if err != nil {
			if _, ok := errors.AsType[*Error](err); !ok {
				t.Fatalf("non-typed error %T: %v", err, err)
			}
			return
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Parse accepted a config that Validate rejects: %v", err)
		}
	})
}
