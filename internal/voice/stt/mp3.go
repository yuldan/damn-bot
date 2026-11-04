package stt

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// mp3Index is the frame map of a CBR/VBR Layer III file. Splitting happens on
// frame boundaries, never on a byte offset, so every chunk is a valid stream.
type mp3Index struct {
	prefix int64 // ID3v2 tag bytes copied into each chunk (may be 0)
	rate   int
	spf    int // samples per frame
	frames []mp3Frame
}

type mp3Frame struct {
	off  int64
	size int
}

// mp3Tables are the Layer III presets needed to walk frames. The caller
// builds them once per file, so they are never mutable globals.
type mp3Tables struct {
	bitrateV1, bitrateV2    [16]int
	rateV1, rateV2, rateV25 [3]int
}

func newMP3Tables() mp3Tables {
	return mp3Tables{
		bitrateV1: [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320},
		bitrateV2: [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160},
		rateV1:    [3]int{44100, 48000, 32000}, rateV2: [3]int{22050, 24000, 16000}, rateV25: [3]int{11025, 12000, 8000},
	}
}

// indexMP3 walks frame headers. ok is false for a malformed or non-Layer-III
// stream; the caller then sends the file whole rather than guessing.
func indexMP3(f *os.File, size int64) (mp3Index, bool) {
	var h [4]byte
	off := id3Prefix(f, size)
	idx := mp3Index{prefix: off}
	tables := newMP3Tables()
	for off+4 <= size {
		if _, err := f.ReadAt(h[:], off); err != nil {
			return mp3Index{}, false
		}
		n, rate, spf, ok := mp3FrameHeader(h[:], &tables)
		if !ok || off+int64(n) > size {
			return mp3Index{}, false
		}
		if idx.rate == 0 {
			idx.rate, idx.spf = rate, spf
		} else if idx.rate != rate || idx.spf != spf {
			return mp3Index{}, false
		}
		idx.frames = append(idx.frames, mp3Frame{off, n})
		off += int64(n)
	}
	if len(idx.frames) == 0 {
		return mp3Index{}, false
	}
	return idx, true
}

// id3Prefix reads an ID3v2 tag length (syncsafe size + 10 header bytes).
func id3Prefix(f *os.File, size int64) int64 {
	var h [10]byte
	if size < 10 {
		return 0
	}
	if _, err := f.ReadAt(h[:], 0); err != nil || string(h[:3]) != "ID3" {
		return 0
	}
	n := int64(h[6])<<21 | int64(h[7])<<14 | int64(h[8])<<7 | int64(h[9])
	if 10+n > size {
		return 0
	}
	return 10 + n
}

// mp3Frame decodes one MPEG Layer III header.
func mp3FrameHeader(h []byte, t *mp3Tables) (length, rate, spf int, ok bool) {
	if h[0] != 0xFF || h[1]&0xE0 != 0xE0 {
		return 0, 0, 0, false
	}
	version := (h[1] >> 3) & 0x03 // 3=MPEG1, 2=MPEG2, 0=MPEG2.5
	layer := (h[1] >> 1) & 0x03   // 1=Layer III
	brIdx := int(h[2] >> 4)
	srIdx := int((h[2] >> 2) & 0x03)
	if version == 1 || layer != 1 || brIdx == 0 || brIdx == 15 || srIdx == 3 {
		return 0, 0, 0, false
	}
	var bitrate, hz int
	switch version {
	case 3:
		bitrate, hz, spf = t.bitrateV1[brIdx], t.rateV1[srIdx], 1152
	case 2:
		bitrate, hz, spf = t.bitrateV2[brIdx], t.rateV2[srIdx], 576
	default:
		bitrate, hz, spf = t.bitrateV2[brIdx], t.rateV25[srIdx], 576
	}
	padding := int((h[2] >> 1) & 1)
	if version == 3 {
		length = 144*bitrate*1000/hz + padding
	} else {
		length = 72*bitrate*1000/hz + padding
	}
	if length < 4 {
		return 0, 0, 0, false
	}
	return length, hz, spf, true
}

// splitMP3 writes frame-aligned chunks at the same time boundaries as WAV.
func splitMP3(f *os.File, size int64, dir string, seconds int) ([]chunk, error) {
	idx, ok := indexMP3(f, size)
	if !ok {
		return []chunk{{path: f.Name()}}, nil
	}
	per := time.Duration(seconds) * time.Second
	var out []chunk
	startFrame := 0
	for startFrame < len(idx.frames) {
		elapsed := time.Duration(0)
		end := startFrame
		for end < len(idx.frames) && (elapsed < per || end == startFrame) {
			elapsed += time.Duration(idx.spf) * time.Second / time.Duration(idx.rate)
			end++
		}
		offset := time.Duration(startFrame) * time.Duration(idx.spf) * time.Second / time.Duration(idx.rate)
		path, err := writeMP3Chunk(f, dir, idx, startFrame, end)
		if err != nil {
			return nil, err
		}
		out = append(out, chunk{path: path, offset: offset})
		if len(out) > MaxChunks {
			return nil, fmt.Errorf("%w: %d chunks > %d", ErrTooLarge, len(out), MaxChunks)
		}
		startFrame = end
	}
	return out, nil
}

func writeMP3Chunk(f *os.File, dir string, idx mp3Index, from, to int) (string, error) {
	if from >= to || to > len(idx.frames) {
		return "", fmt.Errorf("%w: empty MP3 chunk", ErrTooLarge)
	}
	tf, err := os.CreateTemp(dir, "chunk-*.mp3")
	if err != nil {
		return "", fmt.Errorf("voice/stt: chunk: %w", err)
	}
	start := idx.frames[from].off
	end := idx.frames[to-1].off + int64(idx.frames[to-1].size)
	if idx.prefix > 0 {
		if _, cerr := io.CopyN(tf, io.NewSectionReader(f, 0, idx.prefix), idx.prefix); cerr != nil {
			return "", errors.Join(fmt.Errorf("voice/stt: chunk tag: %w", cerr), tf.Close())
		}
	}
	_, werr := io.CopyN(tf, io.NewSectionReader(f, start, end-start), end-start)
	if err := errors.Join(werr, tf.Close()); err != nil {
		return "", fmt.Errorf("voice/stt: chunk: %w", err)
	}
	return tf.Name(), nil
}
