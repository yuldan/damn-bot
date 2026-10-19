package ctxdb

import (
	"context"
	"testing"
)

// TestCreateSessionMetadata: Session.Model/ConfigSHA are persisted instead of
// the old hardcoded 'test/model'/'cfg' placeholder.
func TestCreateSessionMetadata(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s := e.open(1, "tui")
	if _, err := s.CreateSession(context.Background(), Session{
		ID: "S1", Origin: "headless", Cwd: "/work",
		Model: "openai/gpt-4.1", ConfigSHA: "sha256:abc",
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	var model, sha string
	if err := s.Write(context.Background(), func(tx *Tx) error {
		return tx.Get("SELECT model, config_sha FROM sessions WHERE id = ?", []any{"S1"}, &model, &sha)
	}); err != nil {
		t.Fatalf("read session: %v", err)
	}
	if model != "openai/gpt-4.1" || sha != "sha256:abc" {
		t.Fatalf("model/config_sha = %q/%q, want openai/gpt-4.1/sha256:abc", model, sha)
	}
}
