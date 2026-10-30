package telegram

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // register the PNG decoder for image.Decode(Config)
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strings"
	"unicode/utf16"

	"github.com/damnbot/damnbot/internal/gateway/render"
)

// Media is one outbound attachment (SPEC §9.7 bridge `send.media_ref`). Name is
// a display name only: it is sanitized before it reaches the wire and is never
// used as a local path. Data is the raw file; MIME is advisory and only fills
// in when content detection cannot classify the bytes.
type Media struct {
	Name string
	MIME string
	Data []byte
}

// Bot API upload limits and the JPEG quality used when an image is too large
// for sendPhoto (EC-PLAT-023).
const (
	maxPhotoBytes    = 10 << 20
	maxDocumentBytes = 50 << 20
	jpegQuality      = 82
	maxImagePixels   = 64 << 20
	captionLimit     = 1024
)

// ErrMediaTooLarge rejects an attachment above the document limit before any
// bytes are sent.
var ErrMediaTooLarge = errors.New("telegram: media exceeds the 50 MB document limit")

// attachment is one prepared upload: which Bot API method to call, the form
// field that carries the file, and the exact bytes and filename on the wire.
type attachment struct {
	method, field, name, mime string
	data                      []byte
}

// deliverMedia sends one attachment under the outbox key, with the same
// lookup/store rules as a text message: a retry resolves to the stored message
// and an uncertain attempt records a zero marker instead of uploading twice.
func (a *Adapter) deliverMedia(ctx context.Context, out *Outbound) (int64, error) {
	stored, found, err := a.lookup(ctx, out.Key)
	if err != nil {
		return 0, err
	}
	if found {
		return stored, nil
	}
	at, err := a.prepareMedia(out.Media)
	if err != nil {
		return 0, err
	}
	if err := a.send.wait(ctx, out.ChatID); err != nil {
		return 0, err
	}
	id, sendErr := a.sendMedia(ctx, out, &at)
	if errors.Is(sendErr, ErrThreadNotFound) && a.threadFallback == "parent" && out.ThreadID != "" {
		parent := *out
		parent.ThreadID = ""
		parent.Text = "[fallback topik] " + parent.Text
		id, sendErr = a.sendMedia(ctx, &parent, &at)
	}
	if sendErr != nil {
		if out.Key != "" && uncertain(sendErr) {
			if perr := a.store.PutMessageID(ctx, out.Key, 0); perr != nil {
				return 0, fmt.Errorf("telegram: store uncertain marker: %w", perr)
			}
		}
		return 0, sendErr
	}
	if out.Key != "" {
		if err := a.store.PutMessageID(ctx, out.Key, id); err != nil {
			return 0, fmt.Errorf("telegram: store message id: %w", err)
		}
	}
	return id, nil
}

// prepareMedia applies the photo/document policy: a decodable image up to the
// photo limit is uploaded as JPEG (large images are recompressed to fit), any
// other file or an image that cannot be compressed travels as a document. A
// file above 50 MB is rejected and never read further.
func (a *Adapter) prepareMedia(m *Media) (attachment, error) {
	if m == nil || len(m.Data) == 0 {
		return attachment{}, errors.New("telegram: media is empty")
	}
	data, mimeType := m.Data, detectMIME(m.Data)
	if declared, _, err := mime.ParseMediaType(m.MIME); err == nil && mimeType == "application/octet-stream" {
		mimeType = declared
	}
	name := safeName(m.Name, mimeType)
	if len(data) > maxDocumentBytes {
		return attachment{}, fmt.Errorf("%w: %d bytes", ErrMediaTooLarge, len(data))
	}
	if !usableImage(data) {
		return document(data, name, mimeType), nil
	}
	if a.noCompress {
		if len(data) <= maxPhotoBytes {
			return photo(data, name, mimeType), nil
		}
		return document(data, name, mimeType), nil
	}
	if mimeType == "image/jpeg" && len(data) <= maxPhotoBytes && jpegOrientation(data) <= 1 {
		return photo(data, withExt(name, ".jpg"), mimeType), nil
	}
	encoded, err := jpegUnder(data, maxPhotoBytes)
	if err == nil && len(encoded) <= maxPhotoBytes {
		return photo(encoded, withExt(name, ".jpg"), "image/jpeg"), nil
	}
	return document(data, name, mimeType), nil
}

func document(data []byte, name, mimeType string) attachment {
	return attachment{method: "sendDocument", field: "document", name: name, mime: mimeType, data: data}
}

func photo(data []byte, name, mimeType string) attachment {
	return attachment{method: "sendPhoto", field: "photo", name: name, mime: mimeType, data: data}
}

// sendMedia uploads one attachment as multipart and returns the message id.
// The caption is the rendered first chunk, capped to the Bot API limit; no
// parse_mode is set.
func (a *Adapter) sendMedia(ctx context.Context, out *Outbound, at *attachment) (int64, error) {
	fields := url.Values{"chat_id": {out.ChatID}}
	if out.ThreadID != "" {
		fields.Set("message_thread_id", out.ThreadID)
	}
	if anchor := anchorField(out.Anchor); anchor != "" {
		fields.Set("reply_to_message_id", anchor)
	}
	if caption := mediaCaption(out.Text); caption != "" {
		if err := a.checkText(caption); err != nil {
			return 0, err
		}
		fields.Set("caption", caption)
	}
	var sent tgMessage
	ctx, cancel := withDeadline(ctx, a.mediaTimeout)
	defer cancel()
	if err := a.upload(ctx, at.method, fields, at, &sent); err != nil {
		return 0, classifySend(at.method, a.mediaTimeout, err)
	}
	return sent.MessageID, nil
}

// upload writes one multipart form. The token travels only in the request URL;
// neither the field values nor the file bytes are logged here.
func (a *Adapter) upload(ctx context.Context, method string, fields url.Values, at *attachment, out any) error {
	var buf bytes.Buffer
	buf.Grow(len(at.data) + 4096)
	mw := multipart.NewWriter(&buf)
	for key, values := range fields {
		for _, value := range values {
			if err := mw.WriteField(key, value); err != nil {
				return fmt.Errorf("telegram: %s: encode form: %w", method, err)
			}
		}
	}
	disp := mime.FormatMediaType("form-data", map[string]string{"name": at.field, "filename": at.name})
	if disp == "" {
		return fmt.Errorf("telegram: %s: invalid filename", method)
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", disp)
	header.Set("Content-Type", cmp.Or(at.mime, "application/octet-stream"))
	part, err := mw.CreatePart(header)
	if err != nil {
		return fmt.Errorf("telegram: %s: encode file: %w", method, err)
	}
	if _, err := part.Write(at.data); err != nil {
		return fmt.Errorf("telegram: %s: encode file: %w", method, err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("telegram: %s: encode form: %w", method, err)
	}
	return a.post(ctx, method, &buf, mw.FormDataContentType(), out)
}

func mediaCaption(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	caption := renderMessages(text)[0].Text
	if render.Units(caption) <= captionLimit {
		return caption
	}
	units := utf16.Encode([]rune(caption))
	cut := captionLimit
	if units[cut-1] >= 0xD800 && units[cut-1] <= 0xDBFF {
		cut-- // never split a surrogate pair
	}
	return string(utf16.Decode(units[:cut]))
}

// jpegUnder decodes an image, applies its EXIF orientation, and re-encodes it
// as JPEG under limit, lowering quality and then the pixel size. It returns an
// error instead of an oversized result, so the caller can fall back to a
// document rather than fail silently.
func jpegUnder(data []byte, limit int) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("telegram: decode image: %w", err)
	}
	img = applyOrientation(img, jpegOrientation(data))
	var buf bytes.Buffer
	quality := jpegQuality
	for range 12 {
		buf.Reset()
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
			return nil, fmt.Errorf("telegram: encode jpeg: %w", err)
		}
		if buf.Len() <= limit {
			return buf.Bytes(), nil
		}
		if quality > 62 {
			quality -= 8
			continue
		}
		if b := img.Bounds(); b.Dx() <= 128 || b.Dy() <= 128 {
			break
		}
		img = downscale(img, 3, 4)
		quality = 80
	}
	return nil, errors.New("telegram: image cannot be compressed below the photo limit")
}

// usableImage reports whether data is a decodable image small enough to
// decode: the pixel cap keeps a crafted header from turning a 50 MB upload
// into an unbounded allocation.
func usableImage(data []byte) bool {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	return err == nil && cfg.Width > 0 && cfg.Height > 0 &&
		int64(cfg.Width)*int64(cfg.Height) <= maxImagePixels
}

func detectMIME(data []byte) string {
	detected := http.DetectContentType(data)
	if i := strings.IndexByte(detected, ';'); i >= 0 {
		detected = detected[:i]
	}
	return detected
}

// safeName flattens a display name to one path element and drops control
// characters. Traversal segments and leading dots never survive; an empty or
// unusable name falls back to "file" plus an extension derived from the type.
func safeName(name, mimeType string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(strings.TrimLeft(name, "."))
	if name == "" || name == ".." {
		name = "file" + extFor(mimeType)
	}
	if runes := []rune(name); len(runes) > 96 {
		name = string(runes[:96])
	}
	return name
}

func withExt(name, ext string) string {
	return strings.TrimSuffix(name, path.Ext(name)) + ext
}

func extFor(mimeType string) string {
	switch mimeType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	default:
		return ".bin"
	}
}

// downscale resamples to num/den of the source size; it is only used to make a
// JPEG fit the photo limit, so nearest-neighbour is enough.
func downscale(src image.Image, num, den int) image.Image {
	b := src.Bounds()
	w, h := max(1, b.Dx()*num/den), max(1, b.Dy()*num/den)
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		sy := b.Min.Y + y*b.Dy()/h
		for x := range w {
			dst.Set(x, y, src.At(b.Min.X+x*b.Dx()/w, sy))
		}
	}
	return dst
}

// applyOrientation maps the eight EXIF orientations to pixels, so re-encoding
// a phone photo does not rotate it. Orientation 1 is a no-op.
func applyOrientation(src image.Image, orientation int) image.Image {
	if orientation < 2 || orientation > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if orientation >= 5 {
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range h {
		for x := range w {
			var nx, ny int
			switch orientation {
			case 2:
				nx, ny = w-1-x, y
			case 3:
				nx, ny = w-1-x, h-1-y
			case 4:
				nx, ny = x, h-1-y
			case 5:
				nx, ny = y, x
			case 6:
				nx, ny = h-1-y, x
			case 7:
				nx, ny = h-1-y, w-1-x
			case 8:
				nx, ny = y, w-1-x
			}
			dst.Set(nx, ny, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// jpegOrientation reads the EXIF orientation tag of a JPEG without decoding
// the pixels. Missing or malformed metadata means orientation 1.
func jpegOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(data); {
		marker := data[i+1]
		if data[i] != 0xFF || standaloneMarker(marker) {
			i++
			continue
		}
		if marker == 0xDA || marker == 0xD9 {
			break
		}
		length := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if length < 2 || i+2+length > len(data) {
			break
		}
		if marker == 0xE1 {
			if orientation, ok := exifOrientation(data[i+4 : i+2+length]); ok {
				return orientation
			}
		}
		i += 2 + length
	}
	return 1
}

// standaloneMarker reports a JPEG marker without a length field.
func standaloneMarker(marker byte) bool {
	return marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7)
}

// exifOrientation recognises an APP1 payload and reads its TIFF orientation.
func exifOrientation(segment []byte) (int, bool) {
	if len(segment) < 6 || string(segment[:6]) != "Exif\x00\x00" {
		return 0, false
	}
	return tiffOrientation(segment[6:]), true
}

func tiffOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	if order.Uint16(tiff[2:4]) != 42 {
		return 1
	}
	off := int(order.Uint32(tiff[4:8]))
	if off < 0 || off+2 > len(tiff) {
		return 1
	}
	count := int(order.Uint16(tiff[off : off+2]))
	for i := range count {
		entry := off + 2 + i*12
		if entry+12 > len(tiff) {
			break
		}
		if order.Uint16(tiff[entry:entry+2]) != 0x0112 {
			continue
		}
		if order.Uint16(tiff[entry+2:entry+4]) == 3 && order.Uint32(tiff[entry+4:entry+8]) >= 1 {
			if value := int(order.Uint16(tiff[entry+8 : entry+10])); value >= 1 && value <= 8 {
				return value
			}
		}
		return 1
	}
	return 1
}
