package main

import "testing"

func TestNoColorMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name             string
		plain            bool
		noColorEnv, term string
		tty              bool
		want             bool
	}{
		{"plain flag", true, "", "xterm-256color", true, true},
		{"NO_COLOR non-empty", false, "1", "xterm-256color", true, true},
		{"NO_COLOR empty", false, "", "xterm-256color", true, false},
		{"TERM dumb", false, "", "dumb", true, true},
		{"TERM DUMB case", false, "", "DUMB", true, true},
		{"non-TTY", false, "", "xterm-256color", false, true},
		{"colored TTY", false, "", "xterm-256color", true, false},
	}
	for _, tc := range cases {
		if got := noColorMode(tc.plain, tc.noColorEnv, tc.term, tc.tty); got != tc.want {
			t.Fatalf("%s: noColorMode = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRootPlainFlagReachesTUI(t *testing.T) {
	t.Parallel()
	var got []string
	code := rootFlags([]string{"--plain"}, nil, nil, func(args []string) int {
		got = append([]string(nil), args...)
		return 0
	})
	if code != 0 || len(got) != 1 || got[0] != "--plain" {
		t.Fatalf("rootFlags --plain = code %d args %q", code, got)
	}
}
