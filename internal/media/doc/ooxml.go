package doc

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
)

func openZip(data []byte) (*zip.Reader, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fail(CodeUnsupported, "invalid zip container", "")
	}
	if len(zr.File) > maxZipEntries {
		return nil, fail(CodeTooLarge, "zip has more than 10000 entries", "")
	}
	var total uint64
	for _, f := range zr.File {
		name := f.Name
		if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, "\\:\x00") || strings.HasPrefix(name, "/") {
			return nil, fail(CodeInvalidArgs, "unsafe zip entry "+name, "path traversal rejected")
		}
		if f.UncompressedSize64 > maxZipTotal ||
			f.UncompressedSize64 > 0 && (f.CompressedSize64 == 0 || f.UncompressedSize64/f.CompressedSize64 > 100) {
			return nil, fail(CodeTooLarge, "zip entry "+name+" exceeds a zip-bomb limit", "")
		}
		if total += f.UncompressedSize64; total > maxZipTotal {
			return nil, fail(CodeTooLarge, "zip expands beyond 256 MiB", "")
		}
	}
	return zr, nil
}

type zbuf struct {
	zr   *zip.Reader
	left int64
}

func (b *zbuf) get(name string) ([]byte, error) {
	if b.left <= 0 {
		return nil, fail(CodeTooLarge, "zip expands beyond 256 MiB", "")
	}
	for _, f := range b.zr.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fail(CodeUnsupported, "open zip entry "+name, "")
		}
		data, readErr := io.ReadAll(io.LimitReader(rc, b.left+1))
		closeErr := rc.Close()
		if readErr != nil || closeErr != nil || int64(len(data)) > b.left {
			return nil, fail(CodeTooLarge, "zip entry "+name+" exceeds 256 MiB", "")
		}
		b.left -= int64(len(data))
		return data, nil
	}
	return nil, nil
}

func walkXML(data []byte, fn func(xml.Token) error) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fail(CodeUnsupported, "malformed XML", "")
		}
		switch tok.(type) {
		case xml.StartElement:
			if depth++; depth > maxXMLDepth {
				return fail(CodeTooLarge, "XML depth exceeds 256", "")
			}
		case xml.EndElement:
			depth--
		}
		if err := fn(tok); err != nil {
			return err
		}
	}
}

func kindOf(zr *zip.Reader) (Kind, bool) {
	var word, cell, slide bool
	for _, f := range zr.File {
		switch f.Name {
		case "word/document.xml":
			word = true
		case "xl/workbook.xml":
			cell = true
		case "ppt/presentation.xml":
			slide = true
		}
	}
	if word {
		return KindDOCX, true
	}
	if cell {
		return KindXLSX, true
	}
	if slide {
		return KindPPTX, true
	}
	return "", false
}

func ooxmlKind(data []byte) (Kind, bool) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", false
	}
	return kindOf(zr)
}

func readOOXML(data []byte, kind Kind) (Document, error) {
	zr, err := openZip(data)
	if err != nil {
		return Document{}, err
	}
	if kind == "" {
		kind, _ = kindOf(zr)
	}
	if kind == "" {
		return Document{}, fail(CodeUnsupported, "zip is not DOCX/XLSX/PPTX", "")
	}
	b := &zbuf{zr, maxZipTotal}
	var md string
	switch kind {
	case KindDOCX:
		var raw []byte
		if raw, err = b.get("word/document.xml"); err == nil {
			md, err = xmlText(raw, true)
		}
	case KindPPTX:
		md, err = readPPTX(b)
	case KindXLSX:
		md, err = readXLSX(b)
	case KindText, KindPDF:
		return Document{}, fail(CodeUnsupported, string(kind)+" is not an OOXML kind", "")
	}
	if err != nil {
		return Document{}, err
	}
	if strings.TrimSpace(md) == "" {
		return Document{}, fail(CodeNoTextLayer, "document contains no text", "try vision_analyze")
	}
	return Document{Markdown: strings.TrimSpace(md) + "\n"}, nil
}

func readPPTX(b *zbuf) (string, error) {
	var out strings.Builder
	n := 0
	for _, f := range b.zr.File {
		if !strings.HasPrefix(f.Name, "ppt/slides/slide") || !strings.HasSuffix(f.Name, ".xml") {
			continue
		}
		raw, err := b.get(f.Name)
		if err != nil {
			return "", err
		}
		md, err := xmlText(raw, false)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(md) != "" {
			out.WriteString("## Slide " + strconv.Itoa(n+1) + "\n\n" + strings.TrimSpace(md) + "\n\n")
		}
		n++
	}
	return out.String(), nil
}

func xmlText(data []byte, table bool) (string, error) {
	var b strings.Builder
	inText, inCell := false, false
	err := walkXML(data, func(tok xml.Token) error {
		switch e := tok.(type) {
		case xml.StartElement:
			xmlStart(&b, e, table, &inText, &inCell)
		case xml.EndElement:
			xmlEnd(&b, e, table, &inText, &inCell)
		case xml.CharData:
			if inText {
				b.Write(e)
			}
		}
		return nil
	})
	return b.String(), err
}

func xmlStart(b *strings.Builder, e xml.StartElement, table bool, inText, inCell *bool) {
	switch e.Name.Local {
	case "t":
		*inText = true
	case "tab":
		b.WriteByte('\t')
	case "br", "cr":
		b.WriteByte('\n')
	case "tc":
		if table {
			*inCell = true
			b.WriteString("| ")
		}
	}
}

func xmlEnd(b *strings.Builder, e xml.EndElement, table bool, inText, inCell *bool) {
	switch e.Name.Local {
	case "t":
		*inText = false
	case "tc":
		if table {
			*inCell = false
			b.WriteByte(' ')
		}
	case "p":
		if !*inCell {
			b.WriteByte('\n')
		}
	case "tr":
		if table {
			b.WriteByte('\n')
		}
	}
}

func readXLSX(b *zbuf) (string, error) {
	shared, err := sharedStrings(b)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	n := 0
	for _, f := range b.zr.File {
		if !strings.HasPrefix(f.Name, "xl/worksheets/sheet") || !strings.HasSuffix(f.Name, ".xml") {
			continue
		}
		data, err := b.get(f.Name)
		if err != nil {
			return "", err
		}
		rows, cells, err := sheetRows(data, shared)
		if err != nil {
			return "", err
		}
		if cells > 100_000 {
			return "", fail(CodeTooLarge, "sheet has more than 100000 cells", "")
		}
		out.WriteString("## Sheet " + strconv.Itoa(n+1) + "\n\n")
		if len(rows) > 0 {
			out.WriteString(rows[0] + strings.Repeat("| --- ", strings.Count(rows[0], "|")-1) + "|\n")
			for _, row := range rows[1:] {
				out.WriteString(row)
			}
		}
		out.WriteByte('\n')
		n++
	}
	return out.String(), nil
}

func sharedStrings(b *zbuf) ([]string, error) {
	data, err := b.get("xl/sharedStrings.xml")
	if err != nil || data == nil {
		return nil, err
	}
	var out []string
	var cur strings.Builder
	inText := false
	err = walkXML(data, func(tok xml.Token) error {
		switch e := tok.(type) {
		case xml.StartElement:
			if e.Name.Local == "si" {
				cur.Reset()
			}
			inText = e.Name.Local == "t"
		case xml.EndElement:
			if e.Name.Local == "t" {
				inText = false
			}
			if e.Name.Local == "si" {
				out = append(out, cur.String())
			}
		case xml.CharData:
			if inText {
				cur.Write(e)
			}
		}
		return nil
	})
	return out, err
}

func sheetRows(data []byte, shared []string) ([]string, int, error) {
	var rows, row []string
	var val strings.Builder
	typ := ""
	inVal := false
	cells := 0
	err := walkXML(data, func(tok xml.Token) error {
		switch e := tok.(type) {
		case xml.StartElement:
			switch e.Name.Local {
			case "row":
				row = nil
			case "c":
				val.Reset()
				typ = cellType(e)
			case "v":
				inVal = true
			}
		case xml.EndElement:
			switch e.Name.Local {
			case "v":
				inVal = false
			case "c":
				var err error
				if row, err = appendCell(row, typ, val.String(), shared, &cells); err != nil {
					return err
				}
			case "row":
				if len(row) > 0 {
					rows = append(rows, "| "+strings.Join(row, " | ")+" |\n")
				}
			}
		case xml.CharData:
			if inVal {
				val.Write(e)
			}
		}
		return nil
	})
	return rows, cells, err
}

func cellType(e xml.StartElement) string {
	for _, a := range e.Attr {
		if a.Name.Local == "t" {
			return a.Value
		}
	}
	return ""
}

func appendCell(row []string, typ, value string, shared []string, cells *int) ([]string, error) {
	if *cells++; *cells > 100_000 {
		return row, fail(CodeTooLarge, "sheet has more than 100000 cells", "")
	}
	return append(row, xlValue(typ, value, shared)), nil
}

func xlValue(typ, value string, shared []string) string {
	switch typ {
	case "s":
		if i, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && i >= 0 && i < len(shared) {
			return shared[i]
		}
	case "b":
		if value == "1" {
			return "TRUE"
		}
		return "FALSE"
	}
	return value
}
