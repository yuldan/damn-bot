// Package video reads the container header of an inbound video so the media
// pipeline can enforce its size and duration bounds before any decode. Only
// the ISO base media (MP4/MOV) header is understood; an unknown container
// fails closed instead of being forwarded to a model.
package video

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
)

const maxInt = int(^uint(0) >> 1)

// Bounds of one video attachment (SPEC §9.5, TOOL-081/MEDIA-014).
const (
	MaxBytes    = 32 << 20
	MaxDuration = 120.0 // seconds
)

// Sentinel failures. The media module maps them onto the closed tool-code
// vocabulary; the provider repeats the bound as a second gate.
var (
	ErrUnsupported = errors.New("media/video: unsupported container")
	ErrCorrupt     = errors.New("media/video: corrupt container header")
	ErrTooLarge    = errors.New("media/video: too large")
	ErrDuration    = errors.New("media/video: duration exceeds the limit")
)

// Info is the honest post-probe description of one video.
type Info struct {
	MIME      string
	DurationS float64
}

// Probe reads the container header. It never decodes a frame and never
// allocates a second copy of data.
func Probe(data []byte) (Info, error) {
	if len(data) == 0 {
		return Info{}, fmt.Errorf("%w: empty", ErrCorrupt)
	}
	mime := http.DetectContentType(data)
	if mime == "video/mp4" {
		d, ok := mp4Duration(data)
		if !ok {
			return Info{}, fmt.Errorf("%w: no mvhd duration", ErrCorrupt)
		}
		return Info{MIME: mime, DurationS: d}, nil
	}
	if strings.HasPrefix(mime, "application/octet-stream") && mp4Magic(data) {
		d, ok := mp4Duration(data)
		if !ok {
			return Info{}, fmt.Errorf("%w: no mvhd duration", ErrCorrupt)
		}
		return Info{MIME: "video/mp4", DurationS: d}, nil
	}
	return Info{}, fmt.Errorf("%w: %s", ErrUnsupported, mime)
}

// Check applies the hard bounds to one probed video.
func Check(info Info) error {
	if info.DurationS < 0 || math.IsNaN(info.DurationS) || math.IsInf(info.DurationS, 0) {
		return fmt.Errorf("%w: invalid duration", ErrCorrupt)
	}
	if info.DurationS > MaxDuration {
		return fmt.Errorf("%w: %.1fs > %.0fs", ErrDuration, info.DurationS, MaxDuration)
	}
	return nil
}

func mp4Magic(b []byte) bool {
	return len(b) >= 12 && string(b[4:8]) == "ftyp"
}

// mp4Duration walks the top-level boxes for moov/mvhd. Box sizes are
// validated before every slice so a malformed fixture cannot panic.
func mp4Duration(b []byte) (float64, bool) {
	for _, box := range walkBoxes(b) {
		if box.typ != "moov" {
			continue
		}
		for _, child := range walkBoxes(box.body) {
			if child.typ != "mvhd" {
				continue
			}
			return mvhdDuration(child.body)
		}
	}
	return 0, false
}

type box struct {
	typ  string
	body []byte
}

func walkBoxes(b []byte) []box {
	var out []box
	for pos := 0; pos+8 <= len(b); {
		size := int(binary.BigEndian.Uint32(b[pos : pos+4]))
		typ := string(b[pos+4 : pos+8])
		header := 8
		switch size {
		case 0:
			size = len(b) - pos
		case 1:
			if pos+16 > len(b) {
				return out
			}
			large := binary.BigEndian.Uint64(b[pos+8 : pos+16])
			if large > uint64(len(b)-pos) || large > uint64(maxInt) {
				return out
			}
			size, header = int(large), 16
		}
		if size < header || pos+size > len(b) {
			return out
		}
		out = append(out, box{typ: typ, body: b[pos+header : pos+size]})
		pos += size
	}
	return out
}

func mvhdDuration(p []byte) (float64, bool) {
	if len(p) < 4 {
		return 0, false
	}
	version := p[0]
	data := p[4:]
	switch version {
	case 0:
		if len(data) < 16 {
			return 0, false
		}
		scale := binary.BigEndian.Uint32(data[8:12])
		dur := binary.BigEndian.Uint32(data[12:16])
		return ratio(float64(dur), float64(scale))
	case 1:
		if len(data) < 28 {
			return 0, false
		}
		scale := binary.BigEndian.Uint32(data[16:20])
		dur := binary.BigEndian.Uint64(data[20:28])
		return ratio(float64(dur), float64(scale))
	}
	return 0, false
}

func ratio(dur, scale float64) (float64, bool) {
	if scale <= 0 {
		return 0, false
	}
	return dur / scale, true
}
