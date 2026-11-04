package doc

import (
	"encoding/binary"
	"fmt"
	"os"
	"path"
	"runtime"
	"strings"
	"testing"
)

// TestF6GAPTOOL01DocCorpus40 reads a 40-document corpus (10 PDF, 10 DOCX,
// 10 HTML, 10 text) and round-trips 20 written documents byte-for-byte.
func TestF6GAPTOOL01DocCorpus40(t *testing.T) {
	t.Parallel()
	type fixture struct {
		name string
		data []byte
		kind Kind
		want string
	}
	var corpus []fixture
	for i := range 10 {
		marker := fmt.Sprintf("PDF marker %02d", i)
		corpus = append(corpus, fixture{
			name: fmt.Sprintf("pdf-%02d.pdf", i),
			data: buildPDF([]byte("BT ("+marker+") Tj ET"), i%2 == 0, ""),
			kind: KindPDF, want: marker,
		})
		docxMarker := fmt.Sprintf("DOCX marker %02d", i)
		corpus = append(corpus, fixture{
			name: fmt.Sprintf("docx-%02d.docx", i),
			data: mustZip(map[string][]byte{
				"word/document.xml": []byte(`<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>` +
					docxMarker + `</w:t></w:r></w:p></w:body></w:document>`),
			}),
			kind: KindDOCX, want: docxMarker,
		})
		htmlMarker := fmt.Sprintf("HTML marker %02d", i)
		corpus = append(corpus, fixture{
			name: fmt.Sprintf("page-%02d.html", i),
			data: []byte("<!doctype html><html><body><h1>" + htmlMarker + "</h1></body></html>\n"),
			kind: KindText, want: htmlMarker,
		})
		textMarker := fmt.Sprintf("text marker %02d", i)
		corpus = append(corpus, fixture{
			name: fmt.Sprintf("note-%02d.txt", i),
			data: []byte(textMarker + "\n"),
			kind: KindText, want: textMarker,
		})
	}
	if len(corpus) != 40 {
		t.Fatalf("corpus = %d, want 40", len(corpus))
	}
	for _, f := range corpus {
		d, err := ReadBytes(f.data)
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		if d.Kind != f.kind || d.Markdown == "" || !strings.Contains(d.Markdown, f.want) {
			t.Fatalf("%s = kind %q markdown %q, want %q containing %q", f.name, d.Kind, d.Markdown, f.kind, f.want)
		}
	}

	rootDir := t.TempDir()
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			t.Error(cerr)
		}
	}()
	for i := range 20 {
		rel := path.Join(fmt.Sprintf("round-%02d", i%4), fmt.Sprintf("doc-%02d.md", i))
		content := fmt.Sprintf("# round trip %02d\n\nline two\n", i)
		if _, werr := Write(root, WriteRequest{Path: rel, Content: content}); werr != nil {
			t.Fatalf("write %s: %v", rel, werr)
		}
		d, rerr := Read(root, rel)
		if rerr != nil || d.Kind != KindText || d.Markdown != content {
			t.Fatalf("round-trip %s = %+v, %v", rel, d, rerr)
		}
	}
}

// TestF6GAPTOOL02ZipBombRSS: a 10 GiB declared zip is rejected before it is
// expanded, heap growth stays under 100 MB, and the other hard limits
// (PDF streams, XML depth) fail closed.
func TestF6GAPTOOL02ZipBombRSS(t *testing.T) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err := ReadBytes(zipBomb(10 << 30))
	runtime.GC()
	runtime.ReadMemStats(&after)
	if codeOf(t, err) != CodeTooLarge {
		t.Fatalf("bomb code = %v", codeOf(t, err))
	}
	grew := int64(after.HeapAlloc) - int64(before.HeapAlloc) // #nosec G115 -- memstats delta is non-negative
	if grew > 100<<20 {
		t.Fatalf("heap grew %d bytes, want < 100 MB", grew)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 100<<20 {
		t.Fatalf("total allocations grew %d bytes, want < 100 MB", grew)
	}

	if _, err := ReadBytes(pdfStreamsFixture(maxPDFStreams + 1)); codeOf(t, err) != CodeTooLarge {
		t.Fatalf("PDF streams code = %v", codeOf(t, err))
	}
	deep := `<w:document><w:body>` + strings.Repeat("<w:p>", maxXMLDepth) + `<w:r><w:t>x</w:t></w:r>` +
		strings.Repeat("</w:p>", maxXMLDepth) + `</w:body></w:document>`
	if _, err := ReadBytes(mustZip(map[string][]byte{"word/document.xml": []byte(deep)})); codeOf(t, err) != CodeTooLarge {
		t.Fatalf("XML depth code = %v", codeOf(t, err))
	}
}

// zipBomb declares one stored entry of declared bytes and no data: the header
// alone must be rejected, so memory never follows the declared size.
func zipBomb(declared uint64) []byte {
	name := []byte("word/document.xml")
	extra := make([]byte, 20)
	binary.LittleEndian.PutUint16(extra[0:], 0x0001)
	binary.LittleEndian.PutUint16(extra[2:], 16)
	binary.LittleEndian.PutUint64(extra[4:], declared)
	binary.LittleEndian.PutUint64(extra[12:], 0)

	var b []byte
	le := binary.LittleEndian
	put16 := func(v uint16) { b = le.AppendUint16(b, v) }
	put32 := func(v uint32) { b = le.AppendUint32(b, v) }
	put64 := func(v uint64) { b = le.AppendUint64(b, v) }

	put32(0x04034b50) // local file header
	put16(45)
	put16(0)
	put16(0)
	put16(0)
	put16(0)
	put32(0)
	put32(0xFFFFFFFF)
	put32(0xFFFFFFFF)
	put16(uint16(len(name)))  // #nosec G115 -- fixture header
	put16(uint16(len(extra))) // #nosec G115 -- fixture header
	b = append(b, name...)
	b = append(b, extra...)

	cdStart := len(b)
	put32(0x02014b50) // central directory
	put16(45)
	put16(45)
	put16(0)
	put16(0)
	put16(0)
	put16(0)
	put32(0)
	put32(0xFFFFFFFF)
	put32(0xFFFFFFFF)
	put16(uint16(len(name)))  // #nosec G115 -- fixture header
	put16(uint16(len(extra))) // #nosec G115 -- fixture header
	put16(0)
	put16(0)
	put16(0)
	put32(0)
	put32(0)
	b = append(b, name...)
	b = append(b, extra...)
	cdSize := len(b) - cdStart

	z64 := len(b)
	put32(0x06064b50)
	put64(44)
	put16(45)
	put16(45)
	put32(0)
	put32(0)
	put64(1)
	put64(1)
	put64(uint64(cdSize)) // #nosec G115 -- fixture header
	put64(uint64(cdStart))

	put32(0x07064b50) // zip64 locator
	put32(0)
	put64(uint64(z64))
	put32(1)

	put32(0x06054b50) // EOCD
	put16(0)
	put16(0)
	put16(1)
	put16(1)
	put32(uint32(cdSize))  // #nosec G115 -- fixture header
	put32(uint32(cdStart)) // #nosec G115 -- fixture header
	put16(0)
	return b
}

func pdfStreamsFixture(n int) []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	for range n {
		b.WriteString("1 0 obj\n<< /Length 11 >>\nstream\nBT (x) Tj ET\nendstream\nendobj\n")
	}
	b.WriteString("trailer\n<< /Root 1 0 R >>\n%%EOF\n")
	return []byte(b.String())
}

// TestF6DocCorpus and TestF6DocLimits are the ledger names of the corpus and
// limit scenarios above.
func TestF6DocCorpus(t *testing.T) { TestF6GAPTOOL01DocCorpus40(t) }
func TestF6DocLimits(t *testing.T) { TestF6GAPTOOL02ZipBombRSS(t) }
