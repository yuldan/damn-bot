// Package doc reads documents and writes text/markdown inside the workspace (SPEC §9.5).
package doc

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Limits of the document tools (GAP-TOOL-02).
const (
	MaxInputBytes  = 16 << 20
	MaxOutputBytes = 8 << 20
	MaxWriteBytes  = 16 << 20
	maxZipEntries  = 1000
	maxZipTotal    = 64 << 20
	maxXMLDepth    = 256
	maxPDFTotal    = 64 << 20
	maxPDFStreams  = 5000
)

// Kind is a detected document family.
type Kind string

// Document kinds.
const (
	KindText Kind = "text"
	KindPDF  Kind = "pdf"
	KindDOCX Kind = "docx"
	KindXLSX Kind = "xlsx"
	KindPPTX Kind = "pptx"
)

// Document is one extraction result.
type Document struct {
	Kind     Kind
	MIME     string
	Markdown string
	Pages    int
}

// ErrorCode is a typed document failure.
type ErrorCode string

// Error codes from SPEC §9.5.
const (
	CodeInvalidArgs      ErrorCode = "invalid_args"
	CodeTooLarge         ErrorCode = "too_large"
	CodeUnsupported      ErrorCode = "unsupported"
	CodeNoTextLayer      ErrorCode = "no_text_layer"
	CodeOutsideWorkspace ErrorCode = "outside_workspace"
	CodeDenied           ErrorCode = "denied"
)

// Error is a typed document failure.
type Error struct {
	Code    ErrorCode
	Message string
	Hint    string
}

// Error implements error.
func (e *Error) Error() string {
	return fmt.Sprintf("media/doc: %s: %s (%s)", e.Code, e.Message, e.Hint)
}

func fail(code ErrorCode, msg, hint string) error {
	return &Error{Code: code, Message: msg, Hint: hint}
}

// Detect classifies data by magic bytes and container members.
func Detect(data []byte) (Kind, string) {
	switch {
	case bytes.Contains(data[:min(len(data), 1024)], []byte("%PDF-")):
		return KindPDF, "application/pdf"
	case bytes.HasPrefix(data, []byte("PK\x03\x04")), bytes.HasPrefix(data, []byte("PK\x05\x06")):
		if k, ok := ooxmlKind(data); ok {
			return k, ooxmlMIME(k)
		}
		return "", "application/zip"
	case isText(data):
		return KindText, "text/plain; charset=utf-8"
	}
	return "", "application/octet-stream"
}

func isText(data []byte) bool {
	if len(data) == 0 || !utf8.Valid(data) {
		return false
	}
	for _, c := range data {
		if c == 0 || (c < 0x20 && c != '\t' && c != '\n' && c != '\r' && c != '\f') {
			return false
		}
	}
	return true
}

func ooxmlMIME(k Kind) string {
	m := map[Kind]string{
		KindDOCX: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		KindXLSX: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		KindPPTX: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	}
	if s, ok := m[k]; ok {
		return s
	}
	return "application/zip"
}

// Read opens name inside the workspace root and extracts it.
func Read(root *os.Root, name string) (Document, error) {
	if err := validName(name); err != nil {
		return Document{}, err
	}
	st, err := root.Stat(name)
	if err != nil {
		return Document{}, fail(CodeInvalidArgs, "stat "+name+": "+err.Error(), "")
	}
	if st.Size() > MaxInputBytes {
		return Document{}, fail(CodeTooLarge, "document exceeds 16 MiB", "")
	}
	data, err := root.ReadFile(name)
	if err != nil {
		return Document{}, fail(CodeInvalidArgs, "read "+name+": "+err.Error(), "")
	}
	return ReadBytes(data)
}

// ReadBytes extracts an in-memory document.
func ReadBytes(data []byte) (Document, error) {
	if len(data) > MaxInputBytes {
		return Document{}, fail(CodeTooLarge, "document exceeds 16 MiB", "")
	}
	if len(data) == 0 {
		return Document{}, fail(CodeInvalidArgs, "empty document", "")
	}
	kind, mime := Detect(data)
	var doc Document
	var err error
	switch kind {
	case KindText:
		doc = Document{Markdown: strings.TrimPrefix(string(data), "\ufeff")}
	case KindPDF:
		doc, err = readPDF(data)
	case KindDOCX, KindXLSX, KindPPTX:
		doc, err = readOOXML(data, kind)
	default:
		if mime == "application/zip" {
			doc, err = readOOXML(data, "")
		} else {
			err = fail(CodeUnsupported, "unsupported document type "+mime, "")
		}
	}
	if err != nil {
		return Document{}, err
	}
	doc.Kind, doc.MIME = kind, mime
	if len(doc.Markdown) > MaxOutputBytes {
		return Document{}, fail(CodeTooLarge, "extracted text exceeds 8 MiB", "")
	}
	return doc, nil
}

func validName(name string) error {
	if name == "" || name == "." || filepath.IsAbs(name) || !filepath.IsLocal(name) ||
		strings.ContainsAny(name, "\\:\x00") {
		return fail(CodeOutsideWorkspace, "path "+name+" is not a plain workspace path", "")
	}
	return nil
}

// WriteRequest is one doc_write call.
type WriteRequest struct {
	Path      string
	Content   string
	Overwrite bool
	Approved  bool
}

// WriteResult reports what doc_write did.
type WriteResult struct {
	Path    string
	Bytes   int64
	Created bool
}

// Write stores text/markdown inside root; replacing a file needs Overwrite and
// Approved because the tool effect is non-idempotent.
func Write(root *os.Root, req WriteRequest) (WriteResult, error) {
	if err := validName(req.Path); err != nil {
		return WriteResult{}, err
	}
	if len(req.Content) > MaxWriteBytes {
		return WriteResult{}, fail(CodeTooLarge, "document exceeds 16 MiB", "")
	}
	if !utf8.ValidString(req.Content) || strings.ContainsRune(req.Content, 0) {
		return WriteResult{}, fail(CodeInvalidArgs, "doc_write accepts UTF-8 text/markdown only", "")
	}
	st, statErr := root.Stat(req.Path)
	switch {
	case statErr == nil && st.IsDir():
		return WriteResult{}, fail(CodeInvalidArgs, req.Path+" is a directory", "")
	case statErr != nil && !errors.Is(statErr, os.ErrNotExist):
		return WriteResult{}, fail(CodeInvalidArgs, "stat "+req.Path+": "+statErr.Error(), "")
	}
	exists := statErr == nil
	if exists && (!req.Overwrite || !req.Approved) {
		return WriteResult{}, fail(CodeDenied, "refusing to overwrite "+req.Path,
			"set overwrite=true and obtain approval for this non-idempotent call")
	}
	if err := root.MkdirAll(filepath.Dir(req.Path), 0o755); err != nil {
		return WriteResult{}, fail(CodeInvalidArgs, "create directory: "+err.Error(), "")
	}
	if err := root.WriteFile(req.Path, []byte(req.Content), 0o644); err != nil {
		return WriteResult{}, fail(CodeInvalidArgs, "write "+req.Path+": "+err.Error(), "")
	}
	return WriteResult{req.Path, int64(len(req.Content)), !exists}, nil
}
