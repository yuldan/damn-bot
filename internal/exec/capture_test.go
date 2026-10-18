package exec

import (
	"bytes"
	"testing"
)

func TestCaptureHeadTail(t *testing.T) {
	t.Parallel()
	const half = OutputCap / 2
	k := newCapture()
	k.write(bytes.Repeat([]byte("h"), half))
	for i := range 5 {
		k.write(bytes.Repeat([]byte{byte('a' + i)}, half/2+7))
	}
	k.write([]byte("END"))
	out, trunc, total := k.bytes()
	if !trunc || total != int64(half+5*(half/2+7)+3) {
		t.Fatalf("trunc=%v total=%d", trunc, total)
	}
	if len(out) != OutputCap {
		t.Fatalf("len=%d", len(out))
	}
	if !bytes.HasPrefix(out, bytes.Repeat([]byte("h"), half)) || !bytes.HasSuffix(out, []byte("eeeEND")) {
		t.Fatalf("head/tail wrong: %q…%q", out[:8], out[len(out)-8:])
	}
	tail := out[half:]
	if got := bytes.Count(tail, []byte("e")); got != half/2+7 {
		t.Fatalf("e count %d", got)
	}
}

func TestCaptureSmall(t *testing.T) {
	t.Parallel()
	k := newCapture()
	k.write([]byte("abc"))
	k.write([]byte("def"))
	out, trunc, total := k.bytes()
	if string(out) != "abcdef" || trunc || total != 6 {
		t.Fatalf("%q %v %d", out, trunc, total)
	}
}

func TestCaptureHugeWrite(t *testing.T) {
	t.Parallel()
	const half = OutputCap / 2
	k := newCapture()
	big := make([]byte, 3*half)
	for i := range big {
		big[i] = byte(i % 251)
	}
	k.write(big)
	out, trunc, _ := k.bytes()
	if !trunc || !bytes.Equal(out[:half], big[:half]) || !bytes.Equal(out[half:], big[2*half:]) {
		t.Fatal("huge write not kept as head+tail")
	}
}
