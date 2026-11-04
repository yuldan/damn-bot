package doc

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func zipBytes(files map[string][]byte, store bool) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		if store {
			h.Method = zip.Store
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			return nil, fmt.Errorf("zip create %s: %w", name, err)
		}
		if _, err := w.Write(data); err != nil {
			return nil, fmt.Errorf("zip write %s: %w", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("zip close: %w", err)
	}
	return buf.Bytes(), nil
}

func mustZip(files map[string][]byte) []byte {
	data, err := zipBytes(files, false)
	if err != nil {
		return nil
	}
	return data
}

func zipFiles(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	data, err := zipBytes(files, false)
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	return data
}

func docxFixture() []byte {
	return mustZip(map[string][]byte{
		"[Content_Types].xml": []byte(`<Types/>`),
		"word/document.xml": []byte(`<w:document xmlns:w="w"><w:body>` +
			`<w:p><w:r><w:t>Hello DOCX</w:t></w:r></w:p>` +
			`<w:p><w:r><w:t>Second line</w:t></w:r></w:p>` +
			`<w:tbl><w:tr><w:tc><w:p><w:r><w:t>A</w:t></w:r></w:p></w:tc>` +
			`<w:tc><w:p><w:r><w:t>B</w:t></w:r></w:p></w:tc></w:tr></w:tbl>` +
			`</w:body></w:document>`),
	})
}

func xlsxFixture() []byte {
	return mustZip(map[string][]byte{
		"[Content_Types].xml":  []byte(`<Types/>`),
		"xl/workbook.xml":      []byte(`<workbook/>`),
		"xl/sharedStrings.xml": []byte(`<sst><si><t>Name</t></si><si><t>Ada</t></si></sst>`),
		"xl/worksheets/sheet1.xml": []byte(`<worksheet><sheetData>` +
			`<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row>` +
			`<row r="2"><c r="A2"><v>10</v></c><c r="B2"><v>20</v></c></row>` +
			`</sheetData></worksheet>`),
	})
}

func pptxFixture() []byte {
	return mustZip(map[string][]byte{
		"[Content_Types].xml":  []byte(`<Types/>`),
		"ppt/presentation.xml": []byte(`<p:presentation/>`),
		"ppt/slides/slide1.xml": []byte(`<p:sld xmlns:p="p" xmlns:a="a"><p:cSld><p:spTree><p:sp><p:txBody>` +
			`<a:p><a:r><a:t>Slide title</a:t></a:r></a:p>` +
			`<a:p><a:r><a:t>Slide body</a:t></a:r></a:p>` +
			`</p:txBody></p:sp></p:spTree></p:cSld></p:sld>`),
	})
}

func codeOf(t *testing.T, err error) ErrorCode {
	t.Helper()
	var de *Error
	if !errors.As(err, &de) {
		t.Fatalf("error %v is not *Error", err)
	}
	return de.Code
}

func TestDetectHonestMIME(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		data []byte
		kind Kind
		mime string
	}{
		{"pdf-magic", []byte("%PDF-1.4\n"), KindPDF, "application/pdf"},
		{"docx", docxFixture(), KindDOCX, ooxmlMIME(KindDOCX)},
		{"xlsx", xlsxFixture(), KindXLSX, ooxmlMIME(KindXLSX)},
		{"pptx", pptxFixture(), KindPPTX, ooxmlMIME(KindPPTX)},
		{"text", []byte("plain text\n"), KindText, "text/plain; charset=utf-8"},
		{"nul", []byte("text\x00binary"), "", "application/octet-stream"},
		{"invalid-utf8", []byte{0xff, 0xfe, 0xfd}, "", "application/octet-stream"},
		{"plain-zip", zipFiles(t, map[string][]byte{"a.txt": []byte("a")}), "", "application/zip"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			kind, mime := Detect(tc.data)
			if kind != tc.kind || mime != tc.mime {
				t.Fatalf("Detect = %q/%q, want kind=%q mime=%q", kind, mime, tc.kind, tc.mime)
			}
		})
	}
}

func TestReadFormats(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		data []byte
		want []string
	}{
		{"text", []byte("\ufeffhello text\n"), []string{"hello text"}},
		{"docx", docxFixture(), []string{"Hello DOCX", "Second line", "| A | B"}},
		{"xlsx", xlsxFixture(), []string{"Name", "Ada", "10", "| --- |"}},
		{"pptx", pptxFixture(), []string{"## Slide 1", "Slide title", "Slide body"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := ReadBytes(tc.data)
			if err != nil {
				t.Fatalf("ReadBytes: %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(d.Markdown, want) {
					t.Fatalf("markdown %q lacks %q", d.Markdown, want)
				}
			}
		})
	}
}

func TestZipBombGuards(t *testing.T) {
	t.Parallel()
	t.Run("ratio", func(t *testing.T) {
		data := zipFiles(t, map[string][]byte{
			"word/document.xml": bytes.Repeat([]byte("A"), 1<<20),
		})
		if _, err := ReadBytes(data); codeOf(t, err) != CodeTooLarge {
			t.Fatalf("code = %v", codeOf(t, err))
		}
	})
	t.Run("entries", func(t *testing.T) {
		files := map[string][]byte{}
		for i := range 10_001 {
			files[fmt.Sprintf("f%04d.txt", i)] = []byte("x")
		}
		if _, err := ReadBytes(zipFiles(t, files)); codeOf(t, err) != CodeTooLarge {
			t.Fatalf("code = %v", codeOf(t, err))
		}
	})
	t.Run("total", func(t *testing.T) {
		chunk := make([]byte, 1<<20)
		files := map[string][]byte{}
		for i := range 257 {
			files[fmt.Sprintf("blob%03d.bin", i)] = chunk
		}
		if _, err := ReadBytes(mustZip(files)); codeOf(t, err) != CodeTooLarge {
			t.Fatalf("code = %v", codeOf(t, err))
		}
	})
	t.Run("traversal", func(t *testing.T) {
		data := zipFiles(t, map[string][]byte{
			"../evil.xml":       []byte("x"),
			"word/document.xml": []byte("<w:document><w:body><w:p><w:r><w:t>x</w:t></w:r></w:p></w:body></w:document>"),
		})
		if _, err := ReadBytes(data); codeOf(t, err) != CodeInvalidArgs {
			t.Fatalf("code = %v", codeOf(t, err))
		}
	})
	t.Run("malformed", func(t *testing.T) {
		if _, err := ReadBytes([]byte("PK\x03\x04garbage")); codeOf(t, err) != CodeUnsupported {
			t.Fatalf("code = %v", codeOf(t, err))
		}
	})
}

func TestInputSizeLimits(t *testing.T) {
	t.Parallel()
	if _, err := ReadBytes(bytes.Repeat([]byte("A"), MaxInputBytes+1)); codeOf(t, err) != CodeTooLarge {
		t.Fatalf("code = %v", codeOf(t, err))
	}
}

func TestReadWorkspaceRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("root read"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := root.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	d, err := Read(root, "a.txt")
	if err != nil || !strings.Contains(d.Markdown, "root read") {
		t.Fatalf("Read = %+v, %v", d, err)
	}
	if _, err := Read(root, "../a.txt"); codeOf(t, err) != CodeOutsideWorkspace {
		t.Fatalf("code = %v", codeOf(t, err))
	}
}

func TestDocWriteApprovalAndSandbox(t *testing.T) {
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
	first, err := Write(root, WriteRequest{Path: "notes/a.md", Content: "# hi"})
	if err != nil || !first.Created {
		t.Fatalf("create = %+v, %v", first, err)
	}
	if _, werr := Write(root, WriteRequest{Path: "notes/a.md", Content: "x", Overwrite: true}); codeOf(t, werr) != CodeDenied {
		t.Fatalf("no approval code = %v", codeOf(t, werr))
	}
	if _, werr := Write(root, WriteRequest{Path: "notes/a.md", Content: "x", Approved: true}); codeOf(t, werr) != CodeDenied {
		t.Fatalf("no overwrite code = %v", codeOf(t, werr))
	}
	second, err := Write(root, WriteRequest{Path: "notes/a.md", Content: "replaced", Overwrite: true, Approved: true})
	if err != nil || second.Created {
		t.Fatalf("overwrite = %+v, %v", second, err)
	}
	got, err := root.ReadFile("notes/a.md")
	if err != nil || string(got) != "replaced" {
		t.Fatalf("content = %q, %v", got, err)
	}
	if _, err := Write(root, WriteRequest{Path: "../escape", Content: "x"}); codeOf(t, err) != CodeOutsideWorkspace {
		t.Fatalf("escape code = %v", codeOf(t, err))
	}
	if _, err := Write(root, WriteRequest{Path: "bad.md", Content: "a\x00b"}); codeOf(t, err) != CodeInvalidArgs {
		t.Fatalf("NUL code = %v", codeOf(t, err))
	}
	big := strings.Repeat("a", MaxWriteBytes+1)
	if _, err := Write(root, WriteRequest{Path: "big.md", Content: big}); codeOf(t, err) != CodeTooLarge {
		t.Fatalf("big code = %v", codeOf(t, err))
	}
}

func TestDocWriteSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs extra privileges on Windows")
	}
	t.Parallel()
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := root.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	if _, err := Write(root, WriteRequest{Path: "link/evil.txt", Content: "x"}); err == nil {
		t.Fatal("symlink escape was allowed")
	}
}

func FuzzDocRead(f *testing.F) {
	f.Add([]byte("plain text"))
	f.Add(docxFixture())
	f.Add(xlsxFixture())
	f.Add(pptxFixture())
	f.Add([]byte("PK\x03\x04broken"))
	f.Fuzz(func(t *testing.T, data []byte) {
		d, err := ReadBytes(data)
		if err != nil {
			if _, ok := errors.AsType[*Error](err); !ok {
				t.Fatalf("untyped error: %v", err)
			}
			return
		}
		if len(d.Markdown) > MaxOutputBytes {
			t.Fatalf("markdown grew to %d bytes", len(d.Markdown))
		}
		if d.Kind == "" {
			t.Fatal("success without kind")
		}
	})
}
