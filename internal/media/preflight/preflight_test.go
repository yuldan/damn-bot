package preflight

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"testing"
)

func gradient(w, h int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		row := y * img.Stride
		g := uint8(y * 255 / max(1, h-1)) // #nosec G115 -- fraction of 255
		for x := range w {
			i := row + x*4
			img.Pix[i] = uint8(x * 255 / max(1, w-1)) // #nosec G115 -- fraction of 255
			img.Pix[i+1] = g
			img.Pix[i+2] = uint8((x + y) % 256)
			img.Pix[i+3] = 255
		}
	}
	return img
}

func noisy(w, h int, seed int64) *image.NRGBA {
	x := uint32(seed) | 1 // #nosec G115 -- fixture seed
	next := func() uint32 {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		return x
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = uint8(next() & 0xff) // #nosec G115 -- masked to 8 bits
	}
	for i := 3; i < len(img.Pix); i += 4 {
		img.Pix[i] = 255
	}
	return img
}

func encodeJPEG(t testing.TB, img image.Image, quality int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		t.Fatalf("jpeg encode: %v", err)
	}
	return buf.Bytes()
}

func encodePNG(t testing.TB, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}
	return buf.Bytes()
}

func codeOf(t *testing.T, err error) string {
	t.Helper()
	pe, ok := errors.AsType[*Error](err)
	if !ok {
		t.Fatalf("error %v is not *Error", err)
	}
	return pe.Code
}

func TestPreflightDownscalesAndKeepsFormat(t *testing.T) {
	t.Parallel()
	jpg := encodeJPEG(t, gradient(2600, 1950), 80)
	res, err := Preflight(jpg, Default())
	if err != nil {
		t.Fatalf("Preflight JPEG: %v", err)
	}
	if !res.Resized || res.MIME != "image/jpeg" {
		t.Fatalf("result = %+v", res)
	}
	if res.Width > 2048 || res.Height > 2048 || len(res.Data) > 3_750_000 {
		t.Fatalf("limits violated: %+v (%d bytes)", res, len(res.Data))
	}
	if int64(len(res.Data)) > Default().MaxBytes {
		t.Fatal("byte limit violated")
	}
	if res.OriginalWidth != 2600 || res.OriginalHeight != 1950 {
		t.Fatalf("originals = %dx%d", res.OriginalWidth, res.OriginalHeight)
	}
	pngData := encodePNG(t, gradient(1600, 900))
	pres, err := Preflight(pngData, Default())
	if err != nil {
		t.Fatalf("Preflight PNG: %v", err)
	}
	if pres.MIME != "image/png" || pres.Width > 2048 {
		t.Fatalf("png result = %+v", pres)
	}
}

func TestPreflightSmallImageUnchanged(t *testing.T) {
	t.Parallel()
	data := encodeJPEG(t, gradient(64, 48), 80)
	res, err := Preflight(data, Default())
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if res.Resized || !bytes.Equal(res.Data, data) || res.Width != 64 || res.Height != 48 {
		t.Fatalf("small image changed: %+v", res)
	}
}

func TestF6ImageSuite50(t *testing.T) {
	t.Parallel()
	sizeFor := func(i int) [2]int {
		if i%19 == 0 {
			return [2]int{1600, 1200}
		}
		return [2]int{32 + (i*97)%800, 32 + (i*61)%600}
	}
	lim := Default()
	for i := range 50 {
		size := sizeFor(i)
		var data []byte
		switch i % 3 {
		case 0:
			data = encodeJPEG(t, gradient(size[0], size[1]), 70+i%20)
		case 1:
			data = encodePNG(t, gradient(size[0], size[1]))
		default:
			data = encodePNG(t, noisy(min(size[0], 900), min(size[1], 900), int64(i)))
		}
		res, err := Preflight(data, lim)
		if err != nil {
			t.Fatalf("image %d (%dx%d): %v", i, size[0], size[1], err)
		}
		if int64(len(res.Data)) > lim.MaxBytes {
			t.Fatalf("image %d: %d bytes > %d", i, len(res.Data), lim.MaxBytes)
		}
		if res.Width > lim.MaxSide || res.Height > lim.MaxSide ||
			res.Width*res.Height > lim.MaxPixels {
			t.Fatalf("image %d: dims %dx%d exceed preset", i, res.Width, res.Height)
		}
	}
}

func TestPreflightNoisyPNGFitsBytes(t *testing.T) {
	t.Parallel()
	data := encodePNG(t, noisy(900, 900, 7))
	lim := Default()
	lim.MaxBytes = 1_000_000
	res, err := Preflight(data, lim)
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if int64(len(res.Data)) > lim.MaxBytes || res.MIME != "image/png" {
		t.Fatalf("result = %+v (%d bytes)", res, len(res.Data))
	}
}

func TestPreflightIgnoresEXIFOrientation(t *testing.T) {
	t.Parallel()
	plain := encodeJPEG(t, gradient(120, 60), 80)
	oriented := injectEXIFOrientation(plain, 6)
	res, err := Preflight(oriented, Default())
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if res.Width != 120 || res.Height != 60 {
		t.Fatalf("EXIF rotated dimensions: %dx%d", res.Width, res.Height)
	}
}

func injectEXIFOrientation(jpg []byte, orientation uint16) []byte {
	tiff := []byte{'M', 'M', 0, 42, 0, 0, 0, 8, 0, 1}
	entry := make([]byte, 12)
	binary.BigEndian.PutUint16(entry[0:], 0x0112)
	binary.BigEndian.PutUint16(entry[2:], 3)
	binary.BigEndian.PutUint32(entry[4:], 1)
	binary.BigEndian.PutUint16(entry[8:], orientation)
	payload := append([]byte("Exif\x00\x00"), append(tiff, append(entry, 0, 0, 0, 0)...)...)
	app1 := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(app1[2:], uint16(len(payload)+2)) // #nosec G115 -- fixture is tiny
	out := append([]byte{}, jpg[:2]...)
	out = append(out, app1...)
	out = append(out, payload...)
	return append(out, jpg[2:]...)
}

func TestPreflightNegativeControls(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		data []byte
		code string
	}{
		{"gif", []byte("GIF89a"), "unsupported"},
		{"truncated-jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00}, "unsupported"},
		{"truncated-png", []byte("\x89PNG\r\n\x1a\nxxxx"), "unsupported"},
		{"oversize-input", oversizedJPEG(), "too_large"},
		{"png-dimensions", hugePNG(20000, 20000), "too_large"},
		{"bad-limits", encodeJPEG(t, gradient(8, 8), 80), "invalid_args"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lim := Default()
			if tc.name == "bad-limits" {
				lim.MaxSide = 0
			}
			_, err := Preflight(tc.data, lim)
			if err == nil {
				t.Fatal("expected a typed failure")
			}
			if got := codeOf(t, err); got != tc.code {
				t.Fatalf("code = %q, want %q", got, tc.code)
			}
		})
	}
}

func oversizedJPEG() []byte {
	data := make([]byte, MaxInputBytes+1)
	copy(data, []byte{0xFF, 0xD8, 0xFF})
	return data
}

func hugePNG(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8] = 8
	ihdr[9] = 2
	chunk := append([]byte("IHDR"), ihdr...)
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(ihdr))) // #nosec G115 -- IHDR is 13 bytes
	b.Write(length[:])
	b.Write(chunk)
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], crc32.ChecksumIEEE(chunk))
	b.Write(sum[:])
	b.Write([]byte{0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xAE, 0x42, 0x60, 0x82})
	return b.Bytes()
}

func TestPreflightResultString(t *testing.T) {
	t.Parallel()
	res := Result{MIME: "image/jpeg", Width: 10, Height: 20, Data: []byte("xx")}
	if got := res.String(); got == "" {
		t.Fatal("empty String")
	}
}

// specImageTargetMS is the SPEC §17 / F6 acceptance target for a 12 MP
// preflight. It is measured on the Q14 reference machine; a faster or slower
// development host must never lower it or claim the target was met.
const (
	specImageTargetMS         = 150
	specImageReferenceMachine = "Q14"
)

// TestImagePreflightTargetNotLowered pins the acceptance target and preset:
// changing either is a spec decision, not a benchmark tweak.
func TestImagePreflightTargetNotLowered(t *testing.T) {
	t.Parallel()
	if specImageTargetMS != 150 || specImageReferenceMachine != "Q14" {
		t.Fatalf("target = %d ms on %s, want 150 ms on Q14", specImageTargetMS, specImageReferenceMachine)
	}
	if got := Default().MaxPixels; got != 12_000_000 {
		t.Fatalf("MaxPixels = %d, want 12 MP", got)
	}
}

// BenchmarkImagePreflight reports the end-to-end 12 MP preflight. The target
// metric is metadata only: `go test -bench` does not fail a slow host, and
// this host is not Q14, so a passing benchmark is not acceptance evidence.
func BenchmarkImagePreflight(b *testing.B) {
	src := encodeJPEG(b, gradient(4000, 3000), 80)
	lim := Default()
	b.ReportAllocs()
	b.SetBytes(int64(len(src)))
	b.ResetTimer()
	for b.Loop() {
		res, err := Preflight(src, lim)
		if err != nil {
			b.Fatalf("Preflight: %v", err)
		}
		if res.Width > lim.MaxSide || len(res.Data) == 0 {
			b.Fatalf("bad result: %+v", res)
		}
	}
	b.ReportMetric(float64(b.Elapsed().Microseconds())/float64(b.N)/1000, "ms/op")
	b.ReportMetric(specImageTargetMS, "target_ms")
}

// BenchmarkImagePreflightStages splits the same 12 MP JPEG into decode,
// resize, encode and total so a slow stage is visible. The target stays
// 150 ms on Q14; the numbers below are per-host measurements, not a pass.
func BenchmarkImagePreflightStages(b *testing.B) {
	src := encodeJPEG(b, gradient(4000, 3000), 80)
	lim := Default()
	img, _, pngOut, _, err := load(src, lim)
	if err != nil {
		b.Fatalf("load: %v", err)
	}
	scaled := scaleImage(img, lim)
	stage := func(name string, fn func() error) {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if err := fn(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
	stage("decode", func() error {
		_, _, _, _, err := load(src, lim)
		return err
	})
	stage("resize", func() error {
		if scaleImage(img, lim) == nil {
			return fmt.Errorf("nil resize")
		}
		return nil
	})
	stage("encode", func() error {
		_, _, _, _, err := fit(scaled, pngOut, lim)
		return err
	})
	b.Run("total", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(src)))
		for b.Loop() {
			if _, err := Preflight(src, lim); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(specImageTargetMS, "target_ms")
	})
}
