package lsp

import (
	"errors"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestUTF16Positions (EC-FIO-010): LSP positions are UTF-16 code units in
// both directions. A non-BMP rune (emoji, a surrogate pair) advances the
// character offset by two while its UTF-8 byte width is four.
func TestUTF16Positions(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-FIO-010", "F5", "covered") {
		return
	}
	t.Run("EC-FIO-010", func(t *testing.T) {
		t.Parallel()
		text := "a😀b\né中x\n"
		// Byte offsets: a=1, emoji=1..5, b=5, LF=6, é=7..9, 中=9..12,
		// x=12, LF=13.
		cases := []positionCase{
			{0, Position{Line: 0, Character: 0}},
			{1, Position{Line: 0, Character: 1}},
			{5, Position{Line: 0, Character: 3}}, // after the surrogate pair
			{6, Position{Line: 0, Character: 4}},
			{7, Position{Line: 1, Character: 0}},
			{9, Position{Line: 1, Character: 1}},  // é is one UTF-16 unit
			{12, Position{Line: 1, Character: 2}}, // 中 is one UTF-16 unit
			{13, Position{Line: 1, Character: 3}},
			{14, Position{Line: 2, Character: 0}},
		}
		checkPositionRoundTrips(t, text, cases)
		checkPositionErrors(t, text)
	})
}

type positionCase struct {
	offset int
	want   Position
}

// checkPositionRoundTrips proves both directions agree for every boundary.
func checkPositionRoundTrips(t *testing.T, text string, cases []positionCase) {
	t.Helper()
	for _, tc := range cases {
		got, err := PositionAt(text, tc.offset)
		if err != nil || got != tc.want {
			t.Fatalf("PositionAt(%d) = %+v, %v; want %+v", tc.offset, got, err, tc.want)
		}
		back, err := OffsetAt(text, got)
		if err != nil || back != tc.offset {
			t.Fatalf("OffsetAt(%+v) = %d, %v; want %d", got, back, err, tc.offset)
		}
	}
}

// checkPositionErrors holds the negative controls: no position inside a
// surrogate pair, no byte offset inside a UTF-8 sequence.
func checkPositionErrors(t *testing.T, text string) {
	t.Helper()
	// Server→client: the emoji occupies characters 1..3, so character 2 is
	// inside the surrogate pair and cannot name a byte boundary.
	for _, pos := range []Position{{Line: 0, Character: 2}, {Line: 9, Character: 0}, {Line: 0, Character: 9}} {
		if _, err := OffsetAt(text, pos); !errors.Is(err, ErrPosition) {
			t.Fatalf("OffsetAt(%+v) err = %v, want ErrPosition", pos, err)
		}
	}
	// Client→server: a byte offset inside the emoji's UTF-8 sequence is not
	// a character boundary.
	for _, offset := range []int{-1, 2, 3, 4, len(text) + 1} {
		if _, err := PositionAt(text, offset); !errors.Is(err, ErrPosition) {
			t.Fatalf("PositionAt(%d) err = %v, want ErrPosition", offset, err)
		}
	}
	if got, err := OffsetAt("", Position{Line: 0, Character: 0}); err != nil || got != 0 {
		t.Fatalf("empty document = %d, %v", got, err)
	}
	if !strings.Contains(ErrPosition.Error(), "position") {
		t.Fatalf("sentinel text = %q", ErrPosition)
	}
}
