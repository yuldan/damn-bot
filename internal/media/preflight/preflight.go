// Package preflight resizes images to a provider preset before they leave the
// process (SPEC §9.5, MEDIA acceptance). JPEG and PNG are decoded with the
// standard library; EXIF orientation is deliberately ignored (no rotation,
// no dimension swap) because the stdlib does not parse EXIF. A document that
// needs rotation must be normalized by the caller.
package preflight

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"runtime"
	"sync"
)

// MaxInputBytes caps one encoded image.
const MaxInputBytes = 32 << 20

const maxDecodePixels = 100_000_000

// Limits is one provider image preset.
type Limits struct {
	MaxSide   int
	MaxPixels int
	MaxBytes  int64
	Quality   int
}

// Default is the documented provider preset: 2048 px longest side,
// 12 MP, 3.75 MB, JPEG q85 for photos and PNG for graphics.
func Default() Limits {
	return Limits{MaxSide: 2048, MaxPixels: 12_000_000, MaxBytes: 3_750_000, Quality: 85}
}

// Result is one preflighted image. Data is always a complete image under the
// supplied Limits.
type Result struct {
	Data           []byte
	MIME           string
	Width, Height  int
	OriginalWidth  int
	OriginalHeight int
	Resized        bool
}

// Error is a typed preflight failure.
type Error struct {
	Code    string
	Message string
}

// Error implements error.
func (e *Error) Error() string { return "media/preflight: " + e.Code + ": " + e.Message }

func fail(code, msg string) error { return &Error{Code: code, Message: msg} }

// Preflight decodes data and returns an image that satisfies lim. Unsupported
// or malformed input fails closed; it is never forwarded unchanged.
func Preflight(data []byte, lim Limits) (Result, error) {
	img, mime, pngOut, cfg, err := load(data, lim)
	if err != nil {
		return Result{}, err
	}
	res := Result{OriginalWidth: cfg.Width, OriginalHeight: cfg.Height, MIME: mime}
	if cfg.Width <= lim.MaxSide && cfg.Height <= lim.MaxSide &&
		cfg.Width*cfg.Height <= lim.MaxPixels && int64(len(data)) <= lim.MaxBytes {
		res.Data, res.Width, res.Height = data, cfg.Width, cfg.Height
		return res, nil
	}
	scaled := scaleImage(img, lim)
	out, outMIME, w, h, err := fit(scaled, pngOut, lim)
	if err != nil {
		return Result{}, err
	}
	res.Data, res.MIME = out, outMIME
	res.Width, res.Height = w, h
	res.Resized = w != cfg.Width || h != cfg.Height || len(out) != len(data)
	return res, nil
}

func load(data []byte, lim Limits) (image.Image, string, bool, image.Config, error) {
	if lim.MaxSide <= 0 || lim.MaxPixels <= 0 || lim.MaxBytes <= 0 || lim.Quality <= 0 {
		return nil, "", false, image.Config{}, fail("invalid_args", "incomplete limits")
	}
	mime, pngOut, err := detect(data)
	if err != nil {
		return nil, "", false, image.Config{}, err
	}
	if len(data) > MaxInputBytes {
		return nil, "", false, image.Config{}, fail("too_large", "image exceeds 32 MiB")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", false, image.Config{}, fail("unsupported", "cannot decode image header")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, "", false, image.Config{}, fail("invalid_args", "image has empty dimensions")
	}
	if cfg.Width*cfg.Height > maxDecodePixels {
		return nil, "", false, image.Config{}, fail("too_large", "image exceeds 100 MP")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", false, image.Config{}, fail("unsupported", "cannot decode image")
	}
	return img, mime, pngOut, cfg, nil
}

func detect(data []byte) (string, bool, error) {
	switch {
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg", false, nil
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return "image/png", true, nil
	}
	return "", false, fail("unsupported", "not JPEG or PNG")
}

func scaleImage(img image.Image, lim Limits) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	scale := 1.0
	if longest := max(w, h); longest > lim.MaxSide {
		scale = float64(lim.MaxSide) / float64(longest)
	}
	if float64(w)*float64(h)*scale*scale > float64(lim.MaxPixels) {
		scale = min(scale, math.Sqrt(float64(lim.MaxPixels)/(float64(w)*float64(h))))
	}
	dw, dh := max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))
	if dw == w && dh == h {
		return img
	}
	return resizeImage(img, dw, dh)
}

func resizeImage(img image.Image, dw, dh int) image.Image {
	switch src := img.(type) {
	case *image.YCbCr:
		if dst := resizeYCbCr(src, dw, dh); dst != nil {
			return dst
		}
	case *image.Gray:
		return &image.Gray{Pix: resizePlane(src.Pix, src.Stride, src.Rect.Dx(), src.Rect.Dy(), dw, dh), Stride: dw, Rect: image.Rect(0, 0, dw, dh)}
	case *image.NRGBA:
		return resizeNRGBA(src, dw, dh)
	case *image.RGBA:
		// PNG decodes opaque images as RGBA, so the premultiplied bytes equal
		// the straight values. Alpha images arrive as NRGBA.
		return resizeNRGBA(&image.NRGBA{
			Pix: src.Pix, Stride: src.Stride, Rect: src.Rect,
		}, dw, dh)
	}
	return resizeNRGBA(toNRGBA(img), dw, dh)
}

func toNRGBA(img image.Image) *image.NRGBA {
	b := img.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := range b.Dy() {
		for x := range b.Dx() {
			c, ok := color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			if !ok {
				continue
			}
			i := dst.PixOffset(x, y)
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = c.R, c.G, c.B, c.A
		}
	}
	return dst
}

func resizeNRGBA(src *image.NRGBA, dw, dh int) *image.NRGBA {
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	xs := makeSpans(sw, dw)
	ys := makeSpans(sh, dh)
	forEachRow(dh, func(y int) {
		y0, y1 := ys[y].lo, ys[y].hi
		for x := range dw {
			x0, x1 := xs[x].lo, xs[x].hi
			var r, g, b, a, n uint32
			for sy := y0; sy < y1; sy++ {
				row := sy * src.Stride
				for sx := x0; sx < x1; sx++ {
					i := row + sx*4
					r += uint32(src.Pix[i])
					g += uint32(src.Pix[i+1])
					b += uint32(src.Pix[i+2])
					a += uint32(src.Pix[i+3])
					n++
				}
			}
			i := y*dst.Stride + x*4
			dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] =
				uint8(r/n), uint8(g/n), uint8(b/n), uint8(a/n) // #nosec G115 -- average of bytes is <= 255
		}
	})
	return dst
}

func resizeYCbCr(src *image.YCbCr, dw, dh int) image.Image {
	ratio := src.SubsampleRatio
	if ratio != image.YCbCrSubsampleRatio420 && ratio != image.YCbCrSubsampleRatio422 &&
		ratio != image.YCbCrSubsampleRatio444 {
		return nil
	}
	dst := image.NewYCbCr(image.Rect(0, 0, dw, dh), ratio)
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	y := resizePlane(src.Y, src.YStride, sw, sh, dw, dh)
	copyStride(dst.Y, dst.YStride, y, dw, dh)
	cw, ch := chromaSize(sw, sh, ratio)
	dcw, dch := chromaSize(dw, dh, ratio)
	copyStride(dst.Cb, dst.CStride, resizePlane(src.Cb, src.CStride, cw, ch, dcw, dch), dcw, dch)
	copyStride(dst.Cr, dst.CStride, resizePlane(src.Cr, src.CStride, cw, ch, dcw, dch), dcw, dch)
	return dst
}

func chromaSize(w, h int, ratio image.YCbCrSubsampleRatio) (int, int) {
	switch ratio {
	case image.YCbCrSubsampleRatio422:
		return (w + 1) / 2, h
	case image.YCbCrSubsampleRatio420:
		return (w + 1) / 2, (h + 1) / 2
	case image.YCbCrSubsampleRatio444, image.YCbCrSubsampleRatio440,
		image.YCbCrSubsampleRatio411, image.YCbCrSubsampleRatio410:
		return w, h
	}
	return w, h
}

func copyStride(dst []byte, dstStride int, src []byte, w, h int) {
	for y := range h {
		copy(dst[y*dstStride:y*dstStride+w], src[y*w:(y+1)*w])
	}
}

type span struct{ lo, hi int }

func makeSpans(src, dst int) []span {
	out := make([]span, dst)
	for i := range dst {
		lo := i * src / dst
		hi := max(lo+1, (i+1)*src/dst)
		out[i] = span{lo, min(hi, src)}
	}
	return out
}

func forEachRow(h int, fn func(y int)) {
	workers := min(runtime.GOMAXPROCS(0), h)
	if workers <= 1 || h < 256 {
		for y := range h {
			fn(y)
		}
		return
	}
	chunk := (h + workers - 1) / workers
	var wg sync.WaitGroup
	for w := range workers {
		start, end := w*chunk, min((w+1)*chunk, h)
		wg.Go(func() {
			for y := start; y < end; y++ {
				fn(y)
			}
		})
	}
	wg.Wait()
}

func resizePlane(src []byte, stride, sw, sh, dw, dh int) []byte {
	dst := make([]byte, dw*dh)
	xs := makeSpans(sw, dw)
	ys := makeSpans(sh, dh)
	forEachRow(dh, func(y int) {
		y0, y1 := ys[y].lo, ys[y].hi
		for x := range dw {
			x0, x1 := xs[x].lo, xs[x].hi
			var sum, n uint32
			for sy := y0; sy < y1; sy++ {
				row := sy * stride
				for sx := x0; sx < x1; sx++ {
					sum += uint32(src[row+sx])
					n++
				}
			}
			dst[y*dw+x] = uint8(sum / n) // #nosec G115 -- average of bytes is <= 255
		}
	})
	return dst
}

func fit(img image.Image, pngOut bool, lim Limits) ([]byte, string, int, int, error) {
	cur := img
	quality := lim.Quality
	for range 10 {
		data, mime, err := encode(cur, pngOut, quality)
		if err != nil {
			return nil, "", 0, 0, err
		}
		if int64(len(data)) <= lim.MaxBytes {
			return data, mime, cur.Bounds().Dx(), cur.Bounds().Dy(), nil
		}
		if !pngOut && quality > 50 {
			quality -= 10
			continue
		}
		w, h := max(1, cur.Bounds().Dx()*3/4), max(1, cur.Bounds().Dy()*3/4)
		if w == 1 && h == 1 {
			break
		}
		cur = resizeImage(cur, w, h)
	}
	return nil, "", 0, 0, fail("too_large", "image cannot fit the byte limit")
}

func encode(img image.Image, pngOut bool, quality int) ([]byte, string, error) {
	var buf bytes.Buffer
	if pngOut {
		if err := png.Encode(&buf, img); err != nil {
			return nil, "", fail("unsupported", "PNG encode failed")
		}
		return buf.Bytes(), "image/png", nil
	}
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, "", fail("unsupported", "JPEG encode failed")
	}
	return buf.Bytes(), "image/jpeg", nil
}

// String implements fmt.Stringer for logs without dumping image bytes.
func (r Result) String() string {
	return fmt.Sprintf("%s %dx%d (%d bytes)", r.MIME, r.Width, r.Height, len(r.Data))
}
