package media

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/damnbot/damnbot/internal/media/doc"
)

// Deferred tool names (ADR-309, SPEC §9.5).
const (
	ToolDocRead        = "doc_read"
	ToolDocWrite       = "doc_write"
	ToolImageGenerate  = "image_generate"
	ToolVideoGenerate  = "video_generate"
	ToolVisionAnalyze  = "vision_analyze"
	ToolXAIVideoEdit   = "xai_video_edit"
	ToolXAIVideoExtend = "xai_video_extend"
)

// ToolSpec is the module-facing metadata of one deferred tool. The
// composition root maps it onto modules.ToolDescriptor; this package stays a
// leaf module and does not import internal/modules.
type ToolSpec struct {
	Name        string
	Description string
	Tags        []string
	Schema      json.RawMessage
	Effect      string
}

// Tools returns the document tools of this slice.
func Tools() []ToolSpec {
	return []ToolSpec{
		{
			Name:        ToolDocRead,
			Description: "Extract text/markdown from a text, PDF, DOCX, XLSX or PPTX file in the workspace.",
			Tags:        []string{"document", "pdf", "docx", "xlsx", "pptx", "extract"},
			Effect:      "read",
			Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1,"maxLength":512}},"required":["path"],"additionalProperties":false}`),
		},
		{
			Name:        ToolImageGenerate,
			Description: "Generate one image with the configured backend after price approval.",
			Tags:        []string{"image", "picture", "generate", "draw"},
			Effect:      "non_idempotent",
			Schema:      json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string","minLength":1,"maxLength":4000},"size":{"type":"string","maxLength":16}},"required":["prompt"],"additionalProperties":false}`),
		},
		{
			Name:        ToolVideoGenerate,
			Description: "Generate one video with the configured backend after price approval.",
			Tags:        []string{"video", "generate", "clip", "movie"},
			Effect:      "non_idempotent",
			Schema:      json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string","minLength":1,"maxLength":4000},"duration_s":{"type":"integer","minimum":1,"maximum":60}},"required":["prompt"],"additionalProperties":false}`),
		},
		{
			Name:        ToolVisionAnalyze,
			Description: "Analyze one image from the workspace or an http(s) URL with the configured vision model.",
			Tags:        []string{"image", "vision", "picture", "describe", "analyze", "ocr"},
			Effect:      "read",
			Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1,"maxLength":512},"url":{"type":"string","minLength":8,"maxLength":2048},"prompt":{"type":"string","maxLength":2000}},"additionalProperties":false}`),
		},
		{
			Name:        ToolXAIVideoEdit,
			Description: "Edit one existing video with xAI Grok Imagine after price approval.",
			Tags:        []string{"video", "edit", "xai", "grok"},
			Effect:      "non_idempotent",
			Schema:      json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string","minLength":1,"maxLength":4000},"video_url":{"type":"string","minLength":1,"maxLength":4096}},"required":["prompt","video_url"],"additionalProperties":false}`),
		},
		{
			Name:        ToolXAIVideoExtend,
			Description: "Extend one existing video by 2-10 seconds with xAI after price approval.",
			Tags:        []string{"video", "extend", "xai", "grok"},
			Effect:      "non_idempotent",
			Schema:      json.RawMessage(`{"type":"object","properties":{"prompt":{"type":"string","minLength":1,"maxLength":4000},"video_url":{"type":"string","minLength":1,"maxLength":4096},"duration_s":{"type":"integer","minimum":2,"maximum":10}},"required":["prompt","video_url"],"additionalProperties":false}`),
		},
		{
			Name:        ToolDocWrite,
			Description: "Write UTF-8 text/markdown into the workspace; overwrite needs approval.",
			Tags:        []string{"document", "markdown", "write"},
			Effect:      "non_idempotent",
			Schema:      json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","minLength":1,"maxLength":512},"content":{"type":"string","maxLength":16777216},"overwrite":{"type":"boolean"}},"required":["path","content"],"additionalProperties":false}`),
		},
	}
}

// Env carries the core-owned per-call facts this module needs.
type Env struct {
	Approved bool
}

// Result is one tool result, mirroring the module host contract.
type Result struct {
	Content string
	Meta    map[string]any
}

// Invoke runs one document tool inside root. The pipeline owns schema
// validation and approval; Invoke still fails closed on a missing approval
// for an overwrite.
func Invoke(ctx context.Context, root *os.Root, tool string, args json.RawMessage, env Env) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, &doc.Error{Code: doc.CodeInvalidArgs, Message: err.Error()}
	}
	switch tool {
	case ToolDocRead:
		var in struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return Result{}, &doc.Error{Code: doc.CodeInvalidArgs, Message: "invalid doc_read args"}
		}
		d, err := doc.Read(root, in.Path)
		if err != nil {
			return Result{}, fmt.Errorf("media: %w", err)
		}
		return Result{Content: d.Markdown, Meta: map[string]any{
			"mime": d.MIME, "kind": string(d.Kind), "pages": d.Pages,
		}}, nil
	case ToolDocWrite:
		var in struct {
			Path      string `json:"path"`
			Content   string `json:"content"`
			Overwrite bool   `json:"overwrite"`
		}
		if err := json.Unmarshal(args, &in); err != nil {
			return Result{}, &doc.Error{Code: doc.CodeInvalidArgs, Message: "invalid doc_write args"}
		}
		w, err := doc.Write(root, doc.WriteRequest{
			Path: in.Path, Content: in.Content, Overwrite: in.Overwrite, Approved: env.Approved,
		})
		if err != nil {
			return Result{}, fmt.Errorf("media: %w", err)
		}
		return Result{Content: "wrote " + strconv.Itoa(int(w.Bytes)) + " bytes to " + w.Path, Meta: map[string]any{
			"path": w.Path, "bytes": w.Bytes, "created": w.Created,
		}}, nil
	}
	return Result{}, &doc.Error{Code: doc.CodeInvalidArgs, Message: "unknown media tool " + tool}
}
