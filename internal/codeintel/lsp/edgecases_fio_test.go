package lsp

import (
	"testing"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestUTF16RangeConversion (EC-FIO-010): an edit range over non-ASCII text is
// expressed in UTF-16 code units, not bytes or code points. An emoji counts
// as two units, CJK as one, and the conversion round-trips at rune
// boundaries; a byte- or rune-based offset would land inside the emoji.
func TestUTF16RangeConversion(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-FIO-010", "F5", "covered") {
		return
	}
	t.Run("EC-FIO-010", func(t *testing.T) {
		t.Parallel()
		text := []byte("a😀é\n中x")
		cases := []struct {
			offset int
			pos    Position
		}{
			{0, Position{0, 0}},
			{1, Position{0, 1}},  // after "a"
			{5, Position{0, 3}},  // after the emoji (2 UTF-16 units)
			{7, Position{0, 4}},  // after "é"
			{8, Position{1, 0}},  // after "\n"
			{11, Position{1, 1}}, // after "中"
			{12, Position{1, 2}}, // after "x"
		}
		for _, tc := range cases {
			if got := UTF16Position(text, tc.offset); got != tc.pos {
				t.Errorf("UTF16Position(%d) = %+v, want %+v", tc.offset, got, tc.pos)
			}
			if got := ByteOffset(text, tc.pos); got != tc.offset {
				t.Errorf("ByteOffset(%+v) = %d, want %d", tc.pos, got, tc.offset)
			}
		}
		// Negative control: the emoji is one rune but two UTF-16 units, so a
		// code-point count would report Character=2 instead of 3.
		if got := UTF16Position(text, 5); got.Character != 3 {
			t.Fatalf("emoji position = %+v, want UTF-16 character 3", got)
		}
		// Out-of-range values clamp instead of splitting a rune or panicking.
		if got := ByteOffset(text, Position{Line: 9, Character: 9}); got != len(text) {
			t.Fatalf("clamped byte offset = %d, want %d", got, len(text))
		}
		if got := UTF16Position(text, 1<<20); got != (Position{1, 2}) {
			t.Fatalf("clamped position = %+v", got)
		}
	})
}
