package main

import (
	"bytes"
	"testing"
)

func TestRun(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
	}{
		{name: "version", args: []string{"-version"}, wantCode: 0, wantOut: "damnbot dev\n"},
		{name: "no_args", args: nil, wantCode: 2, wantOut: ""},
		{name: "unknown_flag", args: []string{"-nope"}, wantCode: 2, wantOut: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out, errOut bytes.Buffer
			if got := run(tc.args, &out, &errOut); got != tc.wantCode {
				t.Fatalf("run(%q) = %d, want %d; stderr=%q", tc.args, got, tc.wantCode, errOut.String())
			}
			if out.String() != tc.wantOut {
				t.Fatalf("stdout = %q, want %q", out.String(), tc.wantOut)
			}
		})
	}
}
