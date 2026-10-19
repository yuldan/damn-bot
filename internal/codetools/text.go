package codetools

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"unicode/utf16"
	"unicode/utf8"
)

// sniffLen is the head window for encoding and binary detection (CODE-TOOLS
// §2.4, §4.2, CT-34).
const sniffLen = 8 << 10

// Encoding is a text encoding recognized by CODE-TOOLS §4.2. Legacy 8-bit
// encodings are never guessed: they read as UTF-8 with invalid bytes.
type Encoding uint8

// Encodings.
const (
	EncUTF8 Encoding = iota
	EncUTF16LE
	EncUTF16BE
)

// textEnc is the detected encoding of a file plus its BOM.
type textEnc struct {
	enc    Encoding
	hasBOM bool
}

// Byte order marks.
const (
	bomUTF8    = "\xEF\xBB\xBF"
	bomUTF16LE = "\xFF\xFE"
	bomUTF16BE = "\xFE\xFF"
)

// hasPrefix is bytes.HasPrefix against a string without allocating.
func hasPrefix(b []byte, p string) bool { return len(b) >= len(p) && string(b[:len(p)]) == p }

func (t textEnc) bom() string {
	switch {
	case !t.hasBOM:
		return ""
	case t.enc == EncUTF16LE:
		return bomUTF16LE
	case t.enc == EncUTF16BE:
		return bomUTF16BE
	}
	return bomUTF8
}

// String is the header label (utf-8, utf-8 bom, utf-16le bom, …).
func (t textEnc) String() string {
	s := [...]string{"utf-8", "utf-16le", "utf-16be"}[t.enc]
	if t.hasBOM {
		s += " bom"
	}
	return s
}

// detectEnc applies the CODE-TOOLS §4.2 table to the head of a file.
func detectEnc(head []byte) textEnc {
	switch {
	case hasPrefix(head, bomUTF8):
		return textEnc{EncUTF8, true}
	case hasPrefix(head, bomUTF16LE):
		return textEnc{EncUTF16LE, true}
	case hasPrefix(head, bomUTF16BE):
		return textEnc{EncUTF16BE, true}
	}
	if len(head) > sniffLen {
		head = head[:sniffLen]
	}
	if bytes.IndexByte(head, 0) < 0 {
		return textEnc{} // UTF-16 without BOM has NUL bytes in ASCII text
	}
	n := len(head) / 2
	if n == 0 {
		return textEnc{}
	}
	var zeroEven, zeroOdd int
	for i := 0; i+1 < len(head); i += 2 {
		if head[i] == 0 {
			zeroEven++
		}
		if head[i+1] == 0 {
			zeroOdd++
		}
	}
	// ≥ 40% zero bytes at one parity and < 5% at the other.
	switch {
	case zeroOdd*100 >= 40*n && zeroEven*100 < 5*n:
		return textEnc{enc: EncUTF16LE}
	case zeroEven*100 >= 40*n && zeroOdd*100 < 5*n:
		return textEnc{enc: EncUTF16BE}
	}
	return textEnc{}
}

// sniffText classifies a file head for grep in one pass: known magic first,
// then a byte scan. A head with no NUL and no byte >= 0x80 is ASCII (valid
// UTF-8) text, so the decoder and utf8.Valid are skipped for the common
// source file (CT-16); anything else falls back to the full encoding and
// binary tables.
func sniffText(head []byte) (textEnc, bool) {
	if d := magic(head); d != "" {
		return textEnc{}, true
	}
	if len(head) > sniffLen {
		head = head[:sniffLen]
	}
	for _, c := range head {
		if c == 0 || c >= utf8.RuneSelf {
			enc := detectEnc(head)
			bin, _ := sniffBinary(head, enc)
			return enc, bin
		}
	}
	return textEnc{}, false
}

// sniffBinary reports whether head is binary (CODE-TOOLS §2.4, CT-34): a
// known magic, a NUL byte outside detected UTF-16, or more than 10% bytes
// that are not valid UTF-8. desc names the magic ("" when unknown).
func sniffBinary(head []byte, enc textEnc) (binary bool, desc string) {
	if d := magic(head); d != "" {
		return true, d
	}
	if enc.enc != EncUTF8 {
		return false, ""
	}
	if len(head) > sniffLen {
		head = head[:sniffLen]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return true, ""
	}
	if utf8.Valid(head) {
		return false, ""
	}
	invalid := 0
	for i := 0; i < len(head); {
		if head[i] < utf8.RuneSelf {
			i++
			continue
		}
		r, n := utf8.DecodeRune(head[i:])
		if r == utf8.RuneError && n == 1 {
			if !utf8.FullRune(head[i:]) {
				break // rune cut by the window, not invalid
			}
			invalid++
		}
		i += n
	}
	return invalid*10 > len(head), ""
}

// magics lists well-known binary formats by leading bytes (CODE-TOOLS §4.4);
// the first match wins, so more specific entries come first.
type magicSig struct {
	off       int
	sig, desc string
}

func magics() [21]magicSig { // an array: no allocation per sniffed file
	return [...]magicSig{
		{0, "\x89PNG\r\n\x1a\n", "PNG image"},
		{0, "\xff\xd8\xff", "JPEG image"},
		{0, "GIF87a", "GIF image"},
		{0, "GIF89a", "GIF image"},
		{8, "WEBP", "WebP image"},
		{0, "%PDF-", "PDF document"},
		{30, "[Content_Types].xml", "Office document (OOXML)"},
		{0, "PK\x03\x04", "ZIP archive"},
		{0, "PK\x05\x06", "ZIP archive"},
		{0, "\x7fELF\x01", "ELF 32-bit"},
		{0, "\x7fELF\x02", "ELF 64-bit"},
		{0, "\x7fELF", "ELF"},
		{0, "\xfe\xed\xfa\xce", "Mach-O 32-bit"},
		{0, "\xce\xfa\xed\xfe", "Mach-O 32-bit"},
		{0, "\xfe\xed\xfa\xcf", "Mach-O 64-bit"},
		{0, "\xcf\xfa\xed\xfe", "Mach-O 64-bit"},
		{0, "\xca\xfe\xba\xbe", "Mach-O universal or Java class"},
		{0, "MZ", "PE executable"},
		{0, "SQLite format 3\x00", "SQLite database"},
		{0, "\x1f\x8b", "gzip archive"},
		{0, "\x00asm", "WebAssembly module"},
	}
}

// magic names the format of h, or "".
func magic(h []byte) string {
	for _, m := range magics() {
		if len(h) >= m.off && hasPrefix(h[m.off:], m.sig) && (m.off != 8 || hasPrefix(h, "RIFF")) &&
			(m.off != 30 || hasPrefix(h, "PK\x03\x04")) {
			return m.desc
		}
	}
	return ""
}

// humanSize renders a byte count for binary descriptions ("1.2 MB").
func humanSize(n int64) string {
	v, u := float64(n), 0
	for ; v >= 1000 && u < 4; u++ {
		v /= 1000
	}
	if u == 0 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f %cB", v, " kMGT"[u])
}

// cont64 maps 0..63 to the UTF-8 continuation byte 0x80|i.
const cont64 = "\x80\x81\x82\x83\x84\x85\x86\x87\x88\x89\x8a\x8b\x8c\x8d\x8e\x8f" +
	"\x90\x91\x92\x93\x94\x95\x96\x97\x98\x99\x9a\x9b\x9c\x9d\x9e\x9f" +
	"\xa0\xa1\xa2\xa3\xa4\xa5\xa6\xa7\xa8\xa9\xaa\xab\xac\xad\xae\xaf" +
	"\xb0\xb1\xb2\xb3\xb4\xb5\xb6\xb7\xb8\xb9\xba\xbb\xbc\xbd\xbe\xbf"

// decodeUTF16 converts raw UTF-16 (BOM already stripped) to WTF-8: unpaired
// surrogates keep their 3-byte generalized UTF-8 form so encodeUTF16
// restores the exact input; an odd trailing byte is returned separately.
func decodeUTF16(b []byte, be bool) (text, odd []byte) {
	if len(b)%2 == 1 {
		odd = b[len(b)-1:]
		b = b[:len(b)-1]
	}
	order := binary.ByteOrder(binary.LittleEndian)
	if be {
		order = binary.BigEndian
	}
	out := make([]byte, 0, len(b)+len(b)/2)
	for i := 0; i < len(b); i += 2 {
		u := order.Uint16(b[i:])
		if utf16.IsSurrogate(rune(u)) && u < 0xDC00 && i+3 < len(b) {
			if lo := order.Uint16(b[i+2:]); lo >= 0xDC00 && lo <= 0xDFFF {
				out = utf8.AppendRune(out, utf16.DecodeRune(rune(u), rune(lo)))
				i += 2
				continue
			}
		}
		if utf16.IsSurrogate(rune(u)) { // 0xD800..0xDFFF → ED A0..BF 80..BF
			out = append(out, 0xED, cont64[u>>6&0x3F], cont64[u&0x3F])
			continue
		}
		out = utf8.AppendRune(out, rune(u))
	}
	return out, odd
}

// encodeUTF16 is the inverse of decodeUTF16 for WTF-8 input. Bytes that are
// neither UTF-8 nor encoded surrogates cannot come from decodeUTF16; they
// make ok false so callers fail closed instead of writing a lossy file.
func encodeUTF16(s []byte, be bool) (out []byte, ok bool) {
	order := binary.AppendByteOrder(binary.LittleEndian)
	if be {
		order = binary.BigEndian
	}
	out = make([]byte, 0, len(s)*2)
	var units [2]uint16
	for i := 0; i < len(s); {
		if s[i] == 0xED && i+2 < len(s) && s[i+1] >= 0xA0 && s[i+1] <= 0xBF && s[i+2]&0xC0 == 0x80 {
			u := uint16(s[i]&0x0F)<<12 | uint16(s[i+1]&0x3F)<<6 | uint16(s[i+2]&0x3F)
			out = order.AppendUint16(out, u)
			i += 3
			continue
		}
		r, n := utf8.DecodeRune(s[i:])
		if r == utf8.RuneError && n <= 1 {
			return nil, false
		}
		for _, u := range utf16.AppendRune(units[:0], r) {
			out = order.AppendUint16(out, u)
		}
		i += n
	}
	return out, true
}

// escapeInvalid renders a line holding invalid UTF-8 injectively (CODE-TOOLS
// §4.3): invalid bytes as \xNN and literal backslashes as \\. Valid lines
// are returned unchanged with n = 0; U+FFFD is never produced.
func escapeInvalid(line []byte) (out []byte, n int) {
	if utf8.Valid(line) {
		return line, 0
	}
	out = make([]byte, 0, len(line)+16)
	for i := 0; i < len(line); {
		r, size := utf8.DecodeRune(line[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			out = fmt.Appendf(out, `\x%02X`, line[i])
			n++
		case r == '\\':
			out = append(out, '\\', '\\')
		default:
			out = append(out, line[i:i+size]...)
		}
		i += size
	}
	return out, n
}
