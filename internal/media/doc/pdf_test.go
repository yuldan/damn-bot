package doc

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"strings"
	"testing"
)

func deflateBytes(data []byte) []byte {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		return nil
	}
	if err := zw.Close(); err != nil {
		return nil
	}
	return buf.Bytes()
}

func buildPDF(content []byte, filter bool, cmap string) []byte {
	payload := content
	filterPart := ""
	if filter {
		payload = deflateBytes(content)
		filterPart = " /Filter /FlateDecode"
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	b.WriteString("1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n")
	b.WriteString("2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n")
	b.WriteString("3 0 obj\n<< /Type /Page /Parent 2 0 R /Contents 4 0 R >>\nendobj\n")
	fmt.Fprintf(&b, "4 0 obj\n<< /Length %d%s >>\nstream\n", len(payload), filterPart)
	b.Write(payload)
	b.WriteString("\nendstream\nendobj\n")
	if cmap != "" {
		fmt.Fprintf(&b, "5 0 obj\n<< /Length %d >>\nstream\n%s\nendstream\nendobj\n", len(cmap), cmap)
	}
	b.WriteString("trailer\n<< /Root 1 0 R >>\n%%EOF\n")
	return b.Bytes()
}

func TestPDFToUnicodeAndPages(t *testing.T) {
	t.Parallel()
	cmap := "begincmap\n1 begincodespacerange\n<0000> <FFFF>\nendcodespacerange\n" +
		"2 beginbfchar\n<0001> <0048>\n<0002> <0069>\nendbfchar\nendcmap"
	d, err := ReadBytes(buildPDF([]byte("BT <00010002> Tj ET"), true, cmap))
	if err != nil {
		t.Fatalf("ReadBytes: %v", err)
	}
	if !strings.Contains(d.Markdown, "Hi") {
		t.Fatalf("ToUnicode mapping not applied: %q", d.Markdown)
	}
	if d.Pages != 1 {
		t.Fatalf("Pages = %d, want 1", d.Pages)
	}
}

func TestPDFTypedFailures(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		data []byte
		code ErrorCode
	}{
		{"scan", buildPDF([]byte("BT ET"), false, ""), CodeNoTextLayer},
		{"encrypted", append(buildPDF([]byte("BT (x) Tj ET"), false, ""), []byte("\n% /Encrypt")...), CodeUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadBytes(tc.data); codeOf(t, err) != tc.code {
				t.Fatalf("code = %v, want %v", codeOf(t, err), tc.code)
			}
		})
	}
}

func TestPDFSniffedNotByExtension(t *testing.T) {
	t.Parallel()
	pdf := buildPDF([]byte("BT (payload) Tj ET"), false, "")
	if got, _ := Detect(pdf); got != KindPDF {
		t.Fatalf("pdf detection = %+v", got)
	}
}

func TestPDFDecompressionLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates the 64 MiB decompression budget")
	}
	data := buildPDF(bytes.Repeat([]byte("A"), maxPDFTotal+1), true, "")
	if _, err := ReadBytes(data); codeOf(t, err) != CodeTooLarge {
		t.Fatalf("code = %v", codeOf(t, err))
	}
}
