package main

import (
	"testing"

	"github.com/damnbot/damnbot/internal/memory"
)

// TestThreatScanDotenv: the GAP-GW-07 patterns are medium env_file findings,
// never high (a high finding would auto-reject an installable skill).
func TestThreatScanDotenv(t *testing.T) {
	t.Parallel()
	positive := []string{
		"load_dotenv()",
		"dotenv.config()",
		"require('dotenv').config()",
		"cat .env",
		"source .env",
		"grep .env",
	}
	for _, text := range positive {
		scan := memory.ScanText(nil, text)
		if scan.Severity != memory.SeverityMedium {
			t.Errorf("%q = %q, want medium", text, scan.Severity)
		}
		found := false
		for _, f := range scan.Findings {
			if f.Category == "env_file" && f.Severity == memory.SeverityMedium {
				found = true
			}
		}
		if !found {
			t.Errorf("%q = %+v, want an env_file medium finding", text, scan)
		}
		if scan.High() {
			t.Errorf("%q auto-rejects", text)
		}
	}
	for _, text := range []string{"copy .env.example to .env", "os.Getenv(\"HOME\")", "the dotenv format"} {
		if scan := memory.ScanText(nil, text); len(scan.Findings) != 0 {
			t.Errorf("%q = %+v, want no findings", text, scan)
		}
	}
}
