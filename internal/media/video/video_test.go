package video

import (
	"encoding/binary"
	"errors"
	"math"
	"testing"
)

func ftyp(brand string) []byte {
	b := make([]byte, 24)
	binary.BigEndian.PutUint32(b[0:4], 24)
	copy(b[4:8], "ftyp")
	copy(b[8:12], brand)
	binary.BigEndian.PutUint32(b[12:16], 0x200)
	copy(b[16:20], "isom")
	copy(b[20:24], "iso2")
	return b
}

// mkBox writes one box with an explicit constant size, so the fixture avoids
// a runtime int-to-uint32 conversion.
func mkBox(size uint32, typ string, body []byte) []byte {
	b := make([]byte, 8, 8+len(body))
	binary.BigEndian.PutUint32(b[0:4], size)
	copy(b[4:8], typ)
	return append(b, body...)
}

// mvhdV0 builds a version-0 mvhd payload plus its box.
func mvhdV0(durationMS uint32) []byte {
	p := make([]byte, 20)
	binary.BigEndian.PutUint32(p[12:16], 1000) // timescale
	binary.BigEndian.PutUint32(p[16:20], durationMS)
	return mkBox(28, "mvhd", p)
}

func mp4V0(durationMS uint32) []byte {
	return append(ftyp("isom"), mkBox(36, "moov", mvhdV0(durationMS))...)
}

// mvhdV1 uses the 64-bit duration variant.
func mvhdV1(durationMS uint64) []byte {
	p := make([]byte, 32)
	p[0] = 1
	binary.BigEndian.PutUint32(p[20:24], 1000)
	binary.BigEndian.PutUint64(p[24:32], durationMS)
	return mkBox(40, "mvhd", p)
}

func TestProbeMP4Versions(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		data []byte
		want float64
	}{
		{"v0", mp4V0(5000), 5},
		{"v1", append(ftyp("isom"), mkBox(48, "moov", mvhdV1(3500))...), 3.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := Probe(tc.data)
			if err != nil || info.MIME != "video/mp4" || info.DurationS != tc.want {
				t.Fatalf("Probe = %+v, %v; want %v", info, err, tc.want)
			}
		})
	}
}

// A file whose brand is unknown to DetectContentType still probes through the
// ftyp fallback and reports the honest MP4 MIME.
func TestProbeOctetStreamFtypFallback(t *testing.T) {
	t.Parallel()
	data := append(ftyp("zzzz"), mkBox(36, "moov", mvhdV0(2000))...)
	info, err := Probe(data)
	if err != nil || info.MIME != "video/mp4" || info.DurationS != 2 {
		t.Fatalf("Probe = %+v, %v", info, err)
	}
}

func TestProbeRejects(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"empty", nil, ErrCorrupt},
		{"text", []byte("not a video at all"), ErrUnsupported},
		{"ftyp without moov", ftyp("isom"), ErrCorrupt},
		{"mvhd too short", append(ftyp("isom"), mkBox(20, "moov", mkBox(12, "mvhd", []byte{0, 0, 0, 0}))...), ErrCorrupt},
		{"unknown mvhd version", append(ftyp("isom"), mkBox(24, "moov", mkBox(16, "mvhd", []byte{2, 0, 0, 0, 1, 2, 3, 4}))...), ErrCorrupt},
		{"zero timescale", append(ftyp("isom"), mkBox(36, "moov", mvhdV0(1000))...), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, err := Probe(tc.data)
			if tc.name == "zero timescale" {
				// mvhdV0 always writes 1000; corrupt it to zero.
				bad := append([]byte(nil), tc.data...)
				binary.BigEndian.PutUint32(bad[len(bad)-8:len(bad)-4], 0)
				if _, perr := Probe(bad); !errors.Is(perr, ErrCorrupt) {
					t.Fatalf("zero timescale = %v", perr)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("Probe = %+v, %v; want %v", info, err, tc.want)
			}
		})
	}
}

// TestProbeNeverPanics walks every truncation of a valid file: a malformed
// header must fail closed, never panic.
func TestProbeNeverPanics(t *testing.T) {
	t.Parallel()
	data := mp4V0(1000)
	for i := range data {
		if _, err := Probe(data[:i]); err == nil {
			t.Fatalf("prefix %d bytes succeeded", i)
		}
	}
}

func TestWalkBoxesVariants(t *testing.T) {
	t.Parallel()
	// size 0: the box runs to the end of the parent.
	zero := append([]byte{0, 0, 0, 0, 'f', 'r', 'e', 'e'}, []byte("tail")...)
	if got := walkBoxes(zero); len(got) != 1 || got[0].typ != "free" || len(got[0].body) != 4 {
		t.Fatalf("size0 = %+v", got)
	}
	// size 1: 64-bit largesize.
	large := make([]byte, 8)
	binary.BigEndian.PutUint32(large[0:4], 1)
	copy(large[4:8], "uuid")
	large = append(large, make([]byte, 8)...)
	binary.BigEndian.PutUint64(large[8:16], 20)
	large = append(large, []byte("body")...)
	// Fix the offset: header is 16 bytes, so total is 20.
	if got := walkBoxes(large); len(got) != 1 || got[0].typ != "uuid" || len(got[0].body) != 4 {
		t.Fatalf("largesize = %+v", got)
	}
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"short", []byte{0, 0, 0, 8}},
		{"largesize truncated", []byte{0, 0, 0, 1, 'm', 'o', 'o', 'v'}},
		{"largesize overrun", func() []byte {
			b := make([]byte, 16)
			binary.BigEndian.PutUint32(b[0:4], 1)
			copy(b[4:8], "moov")
			binary.BigEndian.PutUint64(b[8:16], 99)
			return b
		}()},
		{"size below header", []byte{0, 0, 0, 4, 'm', 'o', 'o', 'v', 0, 0, 0, 0}},
		{"size overruns parent", []byte{0, 0, 0, 99, 'm', 'o', 'o', 'v'}},
	} {
		if got := walkBoxes(tc.data); len(got) != 0 {
			t.Errorf("%s = %+v, want no boxes", tc.name, got)
		}
	}
}

func TestCheckBounds(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		d    float64
		want error
	}{
		{"zero", 0, nil},
		{"limit", MaxDuration, nil},
		{"negative", -1, ErrCorrupt},
		{"nan", math.NaN(), ErrCorrupt},
		{"inf", math.Inf(1), ErrCorrupt},
		{"over", MaxDuration + 0.5, ErrDuration},
	} {
		if err := Check(Info{DurationS: tc.d}); !errors.Is(err, tc.want) {
			t.Errorf("%s = %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestRatioRejectsZeroScale(t *testing.T) {
	t.Parallel()
	if _, ok := ratio(1, 0); ok {
		t.Fatal("zero scale accepted")
	}
	if _, ok := ratio(1, -1); ok {
		t.Fatal("negative scale accepted")
	}
}
