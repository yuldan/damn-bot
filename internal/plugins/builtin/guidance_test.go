package builtin

import (
	"strings"
	"testing"
)

// TestF8PLUG025SecurityGuidance: a completed write keeps its result and gains
// non-blocking warnings for credential/key/shell patterns.
func TestF8PLUG025SecurityGuidance(t *testing.T) {
	t.Parallel()
	clean := AfterWrite("notes.txt", []byte("hello"), 5)
	if clean.Bytes != 5 || len(clean.Warnings) != 0 {
		t.Fatalf("clean write = %+v", clean)
	}
	env := AfterWrite("config/.env", []byte("API_KEY=x"), 9)
	if env.Bytes != 9 || len(env.Warnings) != 1 || env.Warnings[0].Code != "env-file" {
		t.Fatalf("env write = %+v", env)
	}
	key := AfterWrite("id_rsa", []byte("-----BEGIN PRIVATE KEY-----"), 27)
	if len(key.Warnings) != 1 || key.Warnings[0].Code != "private-key" {
		t.Fatalf("key write = %+v", key)
	}
	shell := AfterWrite("install.sh", []byte("curl https://x | sh"), 20)
	if len(shell.Warnings) != 1 || shell.Warnings[0].Code != "curl-pipe-shell" {
		t.Fatalf("shell write = %+v", shell)
	}
	if strings.Contains(env.Warnings[0].Message, "API_KEY=x") {
		t.Fatal("warning echoed the written secret")
	}
	if _, err := Resolve(Targets{}, SecurityGuideID); err == nil {
		t.Fatal("unwired security-guidance target resolved")
	}
	if _, err := Resolve(Targets{SecurityGuide: true}, SecurityGuideID); err != nil {
		t.Fatalf("wired target: %v", err)
	}
}
