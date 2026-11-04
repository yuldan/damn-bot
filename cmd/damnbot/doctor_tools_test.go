//go:build !minimal

package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/doctor"
)

// TestDoctorToolsCLI pins the `doctor tools` dispatch: `ls` renders the
// embedded lock without network access, `verify` is clean on an empty data
// dir, and both exit 0 without printing a URL.
func TestDoctorToolsCLI(t *testing.T) {
	home := t.TempDir()
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "APPDATA"} {
		t.Setenv(k, home)
	}
	var out, errOut bytes.Buffer
	if code := runDoctor([]string{"tools", "ls", "--json"}, &out, &errOut); code != 0 {
		t.Fatalf("ls = %d: %s", code, errOut.String())
	}
	var rows []doctor.ToolStatus
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("ls json: %v\n%s", err, out.String())
	}
	if len(rows) == 0 || rows[0].Name != "gh" {
		t.Fatalf("rows = %+v", rows)
	}
	if bytes.Contains(out.Bytes(), []byte("https://")) {
		t.Fatalf("ls printed a URL: %s", out.String())
	}

	out.Reset()
	errOut.Reset()
	if code := runDoctor([]string{"tools", "verify"}, &out, &errOut); code != 0 {
		t.Fatalf("verify = %d: %s", code, errOut.String())
	}
	if strings.Contains(out.String(), "https://") {
		t.Fatalf("verify printed a URL: %s", out.String())
	}
}
