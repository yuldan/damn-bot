package main

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestHermesSkillIndexReadsDeveloperMessage: hermes 868b9f244f sends the
// instruction message with role "developer", not "system"; the parser must
// find the catalog there and ignore earlier requests without it.
func TestHermesSkillIndexReadsDeveloperMessage(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "rec.jsonl.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	enc := json.NewEncoder(zw)
	recs := []map[string]any{
		{"method": "POST", "path": "/v1/chat/completions", "req_body": map[string]any{
			"messages": []map[string]string{{"role": "system", "content": "You name chat sessions."}},
		}},
		{"method": "POST", "path": "/v1/chat/completions", "req_body": map[string]any{
			"messages": []map[string]string{
				{"role": "developer", "content": "prefix\n<available_skills>\n  web: browse\n  file: read\n</available_skills>\nsuffix"},
				{"role": "user", "content": "hi"},
			},
		}},
	}
	for _, r := range recs {
		if err := enc.Encode(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	tokens, entries := hermesSkillIndex(path)
	if tokens == 0 || entries != 2 {
		t.Fatalf("tokens = %d entries = %d, want >0 and 2", tokens, entries)
	}
}
