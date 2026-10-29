package telegram

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

// noisePNG builds a deterministic incompressible PNG; the test premise is that
// the file itself is already above the photo limit.
func noisePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if _, err := rand.Read(img.Pix); err != nil {
		t.Fatalf("fill fixture: %v", err)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	if buf.Len() <= maxPhotoBytes {
		t.Fatalf("fixture is %d bytes, want > %d", buf.Len(), maxPhotoBytes)
	}
	return buf.Bytes()
}

func smallPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return buf.Bytes()
}

func TestLargeImageCompressedToPhoto(t *testing.T) {
	data := noisePNG(t, 1800, 1800)
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	id, err := a.Send(t.Context(), &Outbound{
		ChatID: "7", Key: "photo-1",
		Media: &Media{Name: "big.png", Data: data},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if id == 0 {
		t.Fatal("message id = 0")
	}
	if got := len(f.callsFor("sendDocument")); got != 0 {
		t.Fatalf("sendDocument calls = %d, want 0", got)
	}
	calls := f.callsFor("sendPhoto")
	if len(calls) != 1 {
		t.Fatalf("sendPhoto calls = %d, want 1", len(calls))
	}
	up := calls[0].Files["photo"]
	if len(up) == 0 || len(up) > maxPhotoBytes {
		t.Fatalf("uploaded %d bytes, want 1..%d", len(up), maxPhotoBytes)
	}
	if !bytes.HasPrefix(up, []byte{0xFF, 0xD8}) {
		t.Fatalf("upload is not JPEG: %x", up[:min(4, len(up))])
	}
	if calls[0].Names["photo"] != "big.jpg" {
		t.Errorf("photo filename = %q, want big.jpg", calls[0].Names["photo"])
	}
	if _, err := jpeg.DecodeConfig(bytes.NewReader(up)); err != nil {
		t.Fatalf("compressed image does not decode: %v", err)
	}
}

func TestPNGUploadedAsJPEG(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	if _, err := a.Send(t.Context(), &Outbound{ChatID: "7", Media: &Media{Name: "shot.png", Data: smallPNG(t)}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	calls := f.callsFor("sendPhoto")
	if len(calls) != 1 {
		t.Fatalf("sendPhoto calls = %d, want 1", len(calls))
	}
	if !bytes.HasPrefix(calls[0].Files["photo"], []byte{0xFF, 0xD8}) {
		t.Fatal("PNG was not converted to JPEG")
	}
	if calls[0].Names["photo"] != "shot.jpg" {
		t.Errorf("photo filename = %q, want shot.jpg", calls[0].Names["photo"])
	}
}

func TestMediaOverDocumentLimitRejected(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	big := make([]byte, maxDocumentBytes+1)
	_, err := a.Send(t.Context(), &Outbound{ChatID: "7", Media: &Media{Name: "huge.bin", Data: big}})
	if !errors.Is(err, ErrMediaTooLarge) {
		t.Fatalf("error = %v, want ErrMediaTooLarge", err)
	}
	if !strings.Contains(err.Error(), "50 MB") {
		t.Errorf("error = %v, want a clear size limit", err)
	}
	if n := f.requests(); n != 0 {
		t.Fatalf("server saw %d requests, want 0", n)
	}
}

func TestNonImageSentAsDocument(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	body := []byte("plain text attachment")
	if _, err := a.Send(t.Context(), &Outbound{
		ChatID: "7", Text: "lihat ini",
		Media: &Media{Name: "notes.txt", Data: body},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if n := len(f.callsFor("sendPhoto")); n != 0 {
		t.Fatalf("sendPhoto calls = %d, want 0", n)
	}
	calls := f.callsFor("sendDocument")
	if len(calls) != 1 {
		t.Fatalf("sendDocument calls = %d, want 1", len(calls))
	}
	if !bytes.Equal(calls[0].Files["document"], body) {
		t.Fatal("document bytes changed")
	}
	if calls[0].Names["document"] != "notes.txt" {
		t.Errorf("document filename = %q, want notes.txt", calls[0].Names["document"])
	}
	if got := calls[0].Form.Get("caption"); got != "lihat ini" {
		t.Errorf("caption = %q, want lihat ini", got)
	}
}

func TestMediaRetryAfterDropUploadsOnce(t *testing.T) {
	f := newFakeTG(t)
	f.faults["sendPhoto"] = []fakeFault{{Drop: true, Times: 1}}
	a, _ := newTestAdapter(t, f, newMemStore())
	out := &Outbound{ChatID: "7", Key: "media-drop", Media: &Media{Name: "x.png", Data: smallPNG(t)}}
	if _, err := a.Send(t.Context(), out); err == nil {
		t.Fatal("dropped upload reported success")
	}
	if _, err := a.Send(t.Context(), out); !errors.Is(err, ErrUncertain) {
		t.Fatalf("retry error = %v, want ErrUncertain", err)
	}
	if n := len(f.callsFor("sendPhoto")); n != 1 {
		t.Fatalf("sendPhoto calls = %d, want 1 (no duplicate upload)", n)
	}
}

func TestMediaSameKeyUploadsOnce(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	out := &Outbound{ChatID: "7", Key: "media-1", Media: &Media{Name: "x.png", Data: smallPNG(t)}}
	first, err := a.Send(t.Context(), out)
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	second, err := a.Send(t.Context(), out)
	if err != nil {
		t.Fatalf("second send: %v", err)
	}
	if first == 0 || first != second {
		t.Fatalf("ids = %d / %d, want the same stored message", first, second)
	}
	if n := len(f.callsFor("sendPhoto")); n != 1 {
		t.Fatalf("sendPhoto calls = %d, want 1", n)
	}
}

func TestMediaFilenameSanitized(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"../evil.png", "evil.png"},
		{"..\\..\\evil.txt", "evil.txt"},
		{"dir/sub/file.pdf", "file.pdf"},
		{"..", "file.png"},
		{"", "file.png"},
		{"a\x00b.txt", "ab.txt"},
	} {
		if got := safeName(c.in, "image/png"); got != c.want {
			t.Errorf("safeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore())
	if _, err := a.Send(t.Context(), &Outbound{
		ChatID: "7", Media: &Media{Name: "../../evil.txt", Data: []byte("x")},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	name := f.callsFor("sendDocument")[0].Names["document"]
	if strings.ContainsAny(name, `/\`) || name != "evil.txt" {
		t.Fatalf("uploaded filename = %q, want evil.txt", name)
	}
}

// TestNoCompressionUsesDocument is the negative control: with compression
// disabled an oversized image must travel as a document, not fail silently.
func TestNoCompressionUsesDocument(t *testing.T) {
	data := noisePNG(t, 1800, 1800)
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore(), func(c *Config) { c.NoCompression = true })
	if _, err := a.Send(t.Context(), &Outbound{ChatID: "7", Media: &Media{Name: "big.png", Data: data}}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if n := len(f.callsFor("sendPhoto")); n != 0 {
		t.Fatalf("sendPhoto calls = %d, want 0", n)
	}
	calls := f.callsFor("sendDocument")
	if len(calls) != 1 {
		t.Fatalf("sendDocument calls = %d, want 1", len(calls))
	}
	if !bytes.Equal(calls[0].Files["document"], data) {
		t.Fatal("uncompressed fallback changed the bytes")
	}
}

func TestEXIFOrientation(t *testing.T) {
	tiff := make([]byte, 8+2+12)
	copy(tiff, "II")
	binary.LittleEndian.PutUint16(tiff[2:], 42)
	binary.LittleEndian.PutUint32(tiff[4:], 8)
	binary.LittleEndian.PutUint16(tiff[8:], 1)
	binary.LittleEndian.PutUint16(tiff[10:], 0x0112)
	binary.LittleEndian.PutUint16(tiff[12:], 3)
	binary.LittleEndian.PutUint32(tiff[14:], 1)
	binary.LittleEndian.PutUint16(tiff[18:], 6)
	if got := tiffOrientation(tiff); got != 6 {
		t.Fatalf("orientation = %d, want 6", got)
	}
	src := image.NewRGBA(image.Rect(0, 0, 2, 3))
	src.Set(0, 0, color.RGBA{R: 1, A: 255})
	dst := applyOrientation(src, 6)
	if b := dst.Bounds(); b.Dx() != 3 || b.Dy() != 2 {
		t.Fatalf("rotated bounds = %v, want 3x2", b)
	}
	if r, _, _, _ := dst.At(2, 0).RGBA(); r == 0 {
		t.Fatal("orientation 6 did not rotate pixel (0,0) to (2,0)")
	}
}

func TestMediaErrorRedactsToken(t *testing.T) {
	f := newFakeTG(t)
	a, _ := newTestAdapter(t, f, newMemStore(), func(c *Config) {
		c.Doer = &errorDoer{inner: f, err: errWithURL(f.token), remaining: 1}
	})
	_, err := a.Send(t.Context(), &Outbound{ChatID: "7", Media: &Media{Name: "x.png", Data: smallPNG(t)}})
	if err == nil {
		t.Fatal("send succeeded, want a transport error")
	}
	if strings.Contains(err.Error(), f.token) || strings.Contains(err.Error(), "bot"+f.token) {
		t.Fatalf("multipart error leaks the token: %v", err)
	}
	if !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("error = %v, want the token replaced", err)
	}
}

func TestMediaRejectsEmpty(t *testing.T) {
	a, _ := newTestAdapter(t, newFakeTG(t), newMemStore())
	for _, out := range []*Outbound{
		{ChatID: "7", Media: &Media{Name: "x"}},
		{ChatID: "7", Text: "x", Media: &Media{}},
	} {
		if _, err := a.Send(t.Context(), out); err == nil {
			t.Errorf("Send(%+v) succeeded, want error", out)
		}
	}
}
