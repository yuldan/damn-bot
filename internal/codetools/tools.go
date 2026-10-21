package codetools

import (
	"bytes"
	"context"
	"encoding/json"
)

// Eager tool schemas (CODE-TOOLS §2.1, §3, §4.1, §5.2, §5.6; CT-27: each
// ≤ 200 tokens, description ≤ 160 chars).
const (
	schemaGrep = `{"name":"grep","description":"Search file contents (RE2, smart-case). Respects .gitignore. mode: matches|files|count. Output includes file tags for edit_file.",
"parameters":{"type":"object","properties":{"pattern":{"type":"string","minLength":1},"path":{"type":"string"},"glob":{"type":"string"},
"type":{"type":"string","pattern":"^[a-z0-9+#-]{1,16}$"},"case":{"type":"string","enum":["smart","sensitive","insensitive"]},
"context":{"type":"integer","minimum":0,"maximum":5},"mode":{"type":"string","enum":["matches","files","count"]},
"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":500}},"required":["pattern"],"additionalProperties":false}}`
	schemaGlob = `{"name":"glob","description":"List files matching a doublestar pattern. Respects .gitignore. sort: path|mtime (newest first).",
"parameters":{"type":"object","properties":{"pattern":{"type":"string","minLength":1},"path":{"type":"string"},
"sort":{"type":"string","enum":["path","mtime"]},"offset":{"type":"integer","minimum":0},"limit":{"type":"integer","minimum":1,"maximum":2000}},
"required":["pattern"],"additionalProperties":false}}`
	schemaRead = `{"name":"read_file","description":"Read a text file with line numbers. offset<0 reads from end. outline=true lists symbols. Header has tag for edit_file.",
"parameters":{"type":"object","properties":{"path":{"type":"string","minLength":1},"offset":{"type":"integer"},
"limit":{"type":"integer","minimum":1,"maximum":2000},"outline":{"type":"boolean"}},"required":["path"],"additionalProperties":false}}`
	schemaEdit = `{"name":"edit_file","description":"Apply exact edits atomically. Needs tag from read_file/grep. Each edit: old_string+new_string[+replace_all] or start_line+end_line+new_text.",
"parameters":{"type":"object","properties":{"path":{"type":"string","minLength":1},"tag":{"type":"string","pattern":"^[0-9a-f]{8}$"},
"edits":{"type":"array","minItems":1,"maxItems":50,"items":{"type":"object","properties":{"old_string":{"type":"string","minLength":1},
"new_string":{"type":"string"},"replace_all":{"type":"boolean"},"start_line":{"type":"integer"},"end_line":{"type":"integer"},
"new_text":{"type":"string"}},"additionalProperties":false}}},"required":["path","tag","edits"],"additionalProperties":false}}`
	schemaWrite = `{"name":"write_file","description":"Create a file by omitting tag, or overwrite one with the tag from read_file. Keeps encoding, BOM, EOL and mode.",
"parameters":{"type":"object","properties":{"path":{"type":"string","minLength":1},"content":{"type":"string","maxLength":1000000},
"tag":{"type":"string","pattern":"^[0-9a-f]{8}$"}},"required":["path","content"],"additionalProperties":false}}`
)

// Schemas returns the eager file-tool schemas in a fixed order (P15).
func Schemas() []json.RawMessage {
	return []json.RawMessage{
		json.RawMessage(schemaRead), json.RawMessage(schemaWrite), json.RawMessage(schemaEdit),
		json.RawMessage(schemaGlob), json.RawMessage(schemaGrep),
	}
}

// Run dispatches a file tool by name with JSON arguments already validated
// against its schema (SPEC §4.3 langkah 3); unknown fields still fail.
func (e *Engine) Run(ctx context.Context, name string, args json.RawMessage, o Opts) (Result, error) {
	switch name {
	case "read_file":
		return run(ctx, args, o, e.Read)
	case "write_file":
		return run(ctx, args, o, e.Write)
	case "edit_file":
		return run(ctx, args, o, e.Edit)
	case "glob":
		return run(ctx, args, o, e.Glob)
	case "grep":
		return run(ctx, args, o, func(ctx context.Context, a GrepArgs, o Opts) (Result, error) { return e.Grep(ctx, &a, o) })
	}
	return Result{}, toolErr(CodeInvalidArgs, "", "unknown file tool %q", name)
}

// run decodes strict JSON arguments into A and calls the tool.
func run[A any](ctx context.Context, raw json.RawMessage, o Opts, tool func(context.Context, A, Opts) (Result, error)) (Result, error) {
	var a A
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return Result{}, &ToolError{Code: CodeInvalidArgs, Message: "arguments: " + err.Error(), Err: err}
	}
	return tool(ctx, a, o)
}
