package config

import (
	"runtime"
	"testing"
)

func TestOS(t *testing.T) {
	deps := OS()
	if deps.GOOS != runtime.GOOS {
		t.Fatalf("GOOS = %q, want %q", deps.GOOS, runtime.GOOS)
	}
	if deps.ReadFile == nil || deps.Getenv == nil || deps.HomeDir == nil {
		t.Fatalf("OS() returned incomplete deps: %+v", deps)
	}
}
