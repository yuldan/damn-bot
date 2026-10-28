package main

import (
	"testing"

	"github.com/damnbot/damnbot/internal/memory"
)

func TestThreatScanDotenv(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"load_dotenv()", "dotenv.config()", "cat .env", "source .env"} {
		if got := memory.ScanText(nil, text).Severity; got != memory.SeverityMedium {
			t.Errorf("%q = %q, want medium", text, got)
		}
	}
}
