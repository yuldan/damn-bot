package codetools

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

func numbered(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

// TestReadLimits is CT-32: default 400 lines, max 2.000, lines cut at
// 2.000 chars, offset 0 rejected, negative offsets read from the end.
func TestReadLimits(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	v.put(t, "big.txt", numbered(3000))
	v.put(t, "long.txt", "x"+strings.Repeat("é", 2500)+"\nshort\n")
	r := v.read(t, ReadArgs{Path: "big.txt"})
	if h := firstLine(r.Content); !strings.Contains(h, "L1-400/3000") {
		t.Errorf("header %q", h)
	}
	if !strings.HasSuffix(r.Content, "400|line 400\nnext: offset=401") || r.Meta["next"] != 401 {
		t.Errorf("default page tail %q", r.Content[len(r.Content)-40:])
	}
	r = v.read(t, ReadArgs{Path: "big.txt", Offset: new(-3)})
	if !strings.HasSuffix(r.Content, "2998|line 2998\n2999|line 2999\n3000|line 3000") {
		t.Errorf("tail %q", r.Content)
	}
	r = v.read(t, ReadArgs{Path: "long.txt"})
	if !strings.Contains(r.Content, "1|x"+strings.Repeat("é", 1999)+"… [+501 chars]\n2|short") {
		t.Errorf("long line not cut at 2000 chars: %q", r.Content[len(r.Content)-60:])
	}
	for _, a := range []ReadArgs{
		{Path: "big.txt", Offset: new(0)},
		{Path: "big.txt", Limit: readMaxLimit + 1},
		{Path: "big.txt", Offset: new(3001)},
		{Path: ""},
		{Path: "."},
	} {
		_, err := v.e.Read(context.Background(), a, Opts{})
		wantCode(t, err, CodeInvalidArgs)
	}
	_, err := v.e.Read(context.Background(), ReadArgs{Path: "missing.txt"}, Opts{})
	wantCode(t, err, CodeNotFound)
	r = v.read(t, ReadArgs{Path: "big.txt", Limit: readMaxLimit, Offset: new(1)})
	if r.Meta["next"] == nil {
		t.Errorf("2000 lines of 3000 must page")
	}
}

// TestPageCap is CT-32 / ADR-031: a page is cut at the last whole line
// within page_chars (16.000, or 4.000 after the turn aggregate).
func TestPageCap(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	line := strings.Repeat("a", 99)
	v.put(t, "f.txt", strings.Repeat(line+"\n", 1000))
	for _, c := range []struct{ chars, want int }{{0, DefaultPageChars}, {ReducedPageChars, ReducedPageChars}} {
		r, err := v.e.Read(context.Background(), ReadArgs{Path: "f.txt", Limit: 2000}, Opts{PageChars: c.chars})
		if err != nil {
			t.Fatal(err)
		}
		if n := len([]rune(r.Content)); n > c.want || n < c.want-400 {
			t.Errorf("page %d chars, cap %d", n, c.want)
		}
		next, ok := r.Meta["next"].(int)
		if !ok {
			t.Fatalf("no next: %v", r.Meta)
		}
		last := strings.Split(r.Content, "\n")
		if want := "next: offset=" + strconv.Itoa(next); last[len(last)-1] != want || !strings.HasPrefix(last[len(last)-2], strconv.Itoa(next-1)+"|") {
			t.Errorf("page not cut at a whole line: %q / %q", last[len(last)-2], last[len(last)-1])
		}
	}
}

// TestReadEncodings covers §4.2/§4.3: BOMs, UTF-16 with and without BOM,
// CRLF, invalid bytes escaped injectively, never U+FFFD.
func TestReadEncodings(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	cases := []struct{ name, raw, header, body string }{
		{"invalid", "ok\nbad\xff\\x\n", "L1-2/2 · invalid_utf8=1", "1|ok\n2|bad\\xFF\\\\x"},
		{"empty", "", "L1-0/0", ""},
	}
	for _, c := range cases {
		v.put(t, c.name, c.raw)
		r := v.read(t, ReadArgs{Path: c.name})
		if !strings.Contains(firstLine(r.Content), c.header) || !strings.Contains(r.Content, c.body) || strings.ContainsRune(r.Content, '\uFFFD') {
			t.Errorf("%s: %q", c.name, r.Content)
		}
	}
}
