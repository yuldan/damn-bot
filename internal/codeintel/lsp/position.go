package lsp

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// ErrPosition rejects an offset or position outside the document, or one
// that falls inside a UTF-8 sequence or a UTF-16 surrogate pair.
var ErrPosition = errors.New("codeintel/lsp: position out of range")

// Position is an LSP text position: a zero-based line and the offset within
// that line counted in UTF-16 code units (LSP §3.1, EC-FIO-010).
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// PositionAt converts a UTF-8 byte offset in text to an LSP Position. The
// client holds byte offsets; the server expects UTF-16 code units, so a
// non-BMP rune (emoji) counts as two and a BMP rune as one.
func PositionAt(text string, offset int) (Position, error) {
	if offset < 0 || offset > len(text) || (offset < len(text) && !utf8.RuneStart(text[offset])) {
		return Position{}, fmt.Errorf("%w: byte offset %d of %d", ErrPosition, offset, len(text))
	}
	line := strings.Count(text[:offset], "\n")
	lineStart := strings.LastIndexByte(text[:offset], '\n') + 1
	return Position{Line: line, Character: utf16Len(text[lineStart:offset])}, nil
}

// OffsetAt converts an LSP Position expressed in UTF-16 code units to a
// UTF-8 byte offset. A position inside a surrogate pair is rejected instead
// of being rounded to one of its halves.
func OffsetAt(text string, pos Position) (int, error) {
	if pos.Line < 0 || pos.Character < 0 {
		return 0, fmt.Errorf("%w: negative position %+v", ErrPosition, pos)
	}
	start, ok := lineStart(text, pos.Line)
	if !ok {
		return 0, fmt.Errorf("%w: line %d of %d", ErrPosition, pos.Line, strings.Count(text, "\n")+1)
	}
	end := strings.IndexByte(text[start:], '\n')
	if end < 0 {
		end = len(text)
	} else {
		end += start
	}
	units := 0
	for i := start; i < end; {
		r, size := utf8.DecodeRuneInString(text[i:end])
		step := 1
		if r > 0xFFFF {
			step = 2
		}
		if units == pos.Character {
			return i, nil
		}
		if units+step > pos.Character {
			return 0, fmt.Errorf("%w: character %d inside a surrogate pair at line %d", ErrPosition, pos.Character, pos.Line)
		}
		units += step
		i += size
	}
	if units == pos.Character {
		return end, nil
	}
	return 0, fmt.Errorf("%w: character %d past line %d (%d units)", ErrPosition, pos.Character, pos.Line, units)
}

// lineStart returns the byte index where n starts, or false when the document
// has fewer lines.
func lineStart(text string, n int) (int, bool) {
	start := 0
	for range n {
		i := strings.IndexByte(text[start:], '\n')
		if i < 0 {
			return 0, false
		}
		start += i + 1
	}
	return start, true
}

// utf16Len counts the UTF-16 code units of s.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}
