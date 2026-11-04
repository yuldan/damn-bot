package media

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/media/doc"
)

func isCode(err error, code doc.ErrorCode) bool {
	var de *doc.Error
	return errors.As(err, &de) && de.Code == code
}

func TestToolsMetadata(t *testing.T) {
	t.Parallel()
	tools := Tools()
	if len(tools) != 8 {
		t.Fatalf("Tools = %d, want 8", len(tools))
	}
	want := map[string]string{
		ToolDocRead: "read", ToolDocWrite: "non_idempotent",
		ToolImageGenerate: "non_idempotent", ToolVideoGenerate: "non_idempotent",
		ToolVisionAnalyze: "read", ToolVideoAnalyze: "read",
		ToolXAIVideoEdit: "non_idempotent", ToolXAIVideoExtend: "non_idempotent",
	}
	for _, spec := range tools {
		if got, ok := want[spec.Name]; !ok || got != spec.Effect {
			t.Fatalf("tool %q effect %q", spec.Name, spec.Effect)
		}
		if spec.Description == "" || len(spec.Tags) == 0 || !json.Valid(spec.Schema) {
			t.Fatalf("incomplete spec: %+v", spec)
		}
	}
}

func TestInvokeDocumentTools(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := root.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	if werr := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("hello media"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	res, err := Invoke(context.Background(), root, ToolDocRead, json.RawMessage(`{"path":"readme.txt"}`), Env{})
	if err != nil || !strings.Contains(res.Content, "hello media") {
		t.Fatalf("doc_read = %+v, %v", res, err)
	}
	if res.Meta["mime"] != "text/plain; charset=utf-8" {
		t.Fatalf("meta = %v", res.Meta)
	}
	writeArgs := json.RawMessage(`{"path":"out/a.md","content":"# hi"}`)
	if _, err := Invoke(context.Background(), root, ToolDocWrite, writeArgs, Env{}); err != nil {
		t.Fatalf("doc_write: %v", err)
	}
	over := json.RawMessage(`{"path":"out/a.md","content":"replaced","overwrite":true}`)
	if _, err := Invoke(context.Background(), root, ToolDocWrite, over, Env{}); !isCode(err, doc.CodeDenied) {
		t.Fatalf("overwrite without approval = %v", err)
	}
	if _, err := Invoke(context.Background(), root, ToolDocWrite, over, Env{Approved: true}); err != nil {
		t.Fatalf("approved overwrite: %v", err)
	}
	if _, err := Invoke(context.Background(), root, "nope", nil, Env{}); !isCode(err, doc.CodeInvalidArgs) {
		t.Fatalf("unknown tool = %v", err)
	}
	if _, err := Invoke(context.Background(), root, ToolDocRead, json.RawMessage(`{"path":"../x"}`), Env{}); !isCode(err, doc.CodeOutsideWorkspace) {
		t.Fatalf("escape = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Invoke(ctx, root, ToolDocRead, json.RawMessage(`{"path":"readme.txt"}`), Env{}); err == nil {
		t.Fatal("canceled context succeeded")
	}
}
