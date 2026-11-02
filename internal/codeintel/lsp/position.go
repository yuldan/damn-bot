package lsp

import "unicode/utf8"

// Position is an LSP text position: zero-based line and UTF-16 code-unit
// character offset. LSP ranges are UTF-16 even though Go strings are UTF-8;
// ByteOffset and UTF16Position are the only conversion boundary.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is one LSP range.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// UTF16Position converts a Go byte offset into an LSP position. An offset
// outside the text is clamped; a byte inside a multi-byte rune is treated as
// that rune's start so no position points into the middle of a code point.
func UTF16Position(text []byte, offset int) Position {
	if offset < 0 {
		offset = 0
	}
	if offset > len(text) {
		offset = len(text)
	}
	pos := Position{}
	for i := 0; i < offset; {
		r, size := utf8.DecodeRune(text[i:])
		if i+size > offset {
			size = offset - i // clamp into the rune, not past it
		}
		if r == '\n' {
			pos.Line++
			pos.Character = 0
		} else {
			pos.Character += utf16Len(r)
		}
		i += size
	}
	return pos
}

// ByteOffset converts an LSP position into a Go byte offset. A position past
// the requested line, or past the line's last code unit, is clamped to the
// line end; a byte offset never splits a UTF-8 rune.
func ByteOffset(text []byte, pos Position) int {
	if pos.Line < 0 {
		return 0
	}
	line, i := 0, 0
	for line < pos.Line && i < len(text) {
		if text[i] == '\n' {
			line++
		}
		i++
	}
	if line != pos.Line {
		return len(text)
	}
	units := 0
	for i < len(text) && text[i] != '\n' && units < pos.Character {
		r, size := utf8.DecodeRune(text[i:])
		units += utf16Len(r)
		i += size
	}
	return i
}

// utf16Len is the UTF-16 code-unit width of one rune: 2 for supplementary
// plane code points, 1 otherwise (including U+FFFD for invalid UTF-8).
func utf16Len(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}
