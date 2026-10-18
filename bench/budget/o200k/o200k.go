// Package o200k adalah tokenizer BPE o200k_base (tiktoken) offline, test/harness-only (SPEC §5.3, P1).
//
// File rank di-embed (gzip) dari
// https://openaipublic.blob.core.windows.net/encodings/o200k_base.tiktoken
// SHA-256 (tanpa gzip) = RanksSHA256; sama dengan expected_hash di tiktoken (openai_public.py).
// Token khusus (<|endoftext|> dll.) tidak dikenali: teks diperlakukan sebagai teks biasa,
// sama dengan tiktoken `encode(text, disallowed_special=())`.
package o200k

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"sync"
	"unicode/utf8"
)

// RanksSHA256 = SHA-256 file o200k_base.tiktoken asli.
const RanksSHA256 = "446a9538cb6c348e3516120d7c08b09f57c36495e2acfffe59a5bf8b0cfb1a2d"

// numRanks = jumlah token mergeable o200k_base.
const numRanks = 199998

//go:embed o200k_base.tiktoken.gz
var ranksGz []byte

// ws = kelas Unicode White_Space (setara `\s` regex Rust/fancy-regex yang dipakai tiktoken);
// `\s` di RE2/Go hanya ASCII, jadi kelas ditulis eksplisit.
const ws = `\t\n\v\f\r \x{85}\x{A0}\x{1680}\x{2000}-\x{200A}\x{2028}\x{2029}\x{202F}\x{205F}\x{3000}`

// pat = pola pre-tokenisasi o200k_base tanpa dua alternatif terakhir (`\s+(?!\S)|\s+`),
// yang butuh lookahead (tidak ada di RE2) dan ditangani manual di splitWhitespace.
// Regexp Go memilih match yang sama dengan mesin backtracking (leftmost-first), jadi
// urutan alternatif identik dengan pola asli tiktoken.
var pat = regexp.MustCompile(`\A(?:` +
	`[^\r\n\p{L}\p{N}]?[\p{Lu}\p{Lt}\p{Lm}\p{Lo}\p{M}]*[\p{Ll}\p{Lm}\p{Lo}\p{M}]+(?i:'s|'t|'re|'ve|'m|'ll|'d)?` +
	`|[^\r\n\p{L}\p{N}]?[\p{Lu}\p{Lt}\p{Lm}\p{Lo}\p{M}]+[\p{Ll}\p{Lm}\p{Lo}\p{M}]*(?i:'s|'t|'re|'ve|'m|'ll|'d)?` +
	`|\p{N}{1,3}` +
	`| ?[^` + ws + `\p{L}\p{N}]+[\r\n/]*` +
	`|[` + ws + `]*[\r\n]+` +
	`)`)

// Tokenizer menyimpan tabel rank; aman dipakai bersamaan (read-only).
type Tokenizer struct {
	ranks map[string]int
}

// Load mem-parse tabel rank yang di-embed sekali.
var Load = sync.OnceValues(func() (*Tokenizer, error) { return parse(ranksGz) })

func parse(gz []byte) (*Tokenizer, error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("o200k: gzip: %w", err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("o200k: gunzip: %w", err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != RanksSHA256 {
		return nil, fmt.Errorf("o200k: sha256 %s != %s", got, RanksSHA256)
	}
	ranks := make(map[string]int, numRanks)
	sc := bufio.NewScanner(bytes.NewReader(raw))
	for sc.Scan() {
		b64, rankStr, ok := bytes.Cut(sc.Bytes(), []byte{' '})
		if !ok {
			return nil, errors.New("o200k: baris rank tanpa spasi")
		}
		tok, err := base64.StdEncoding.DecodeString(string(b64))
		if err != nil {
			return nil, fmt.Errorf("o200k: base64: %w", err)
		}
		rank, err := strconv.Atoi(string(rankStr))
		if err != nil {
			return nil, fmt.Errorf("o200k: rank: %w", err)
		}
		ranks[string(tok)] = rank
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("o200k: scan: %w", err)
	}
	if len(ranks) != numRanks {
		return nil, fmt.Errorf("o200k: %d rank, want %d", len(ranks), numRanks)
	}
	return &Tokenizer{ranks: ranks}, nil
}

// Count mengembalikan jumlah token o200k_base untuk text.
func (t *Tokenizer) Count(text string) int {
	n := 0
	for piece := range pieces(text) {
		if _, ok := t.ranks[piece]; ok {
			n++
			continue
		}
		n += len(t.merge(piece))
	}
	return n
}

// Encode mengembalikan id token o200k_base untuk text (dipakai test kesetaraan dengan tiktoken).
func (t *Tokenizer) Encode(text string) []int {
	var ids []int
	for piece := range pieces(text) {
		if r, ok := t.ranks[piece]; ok {
			ids = append(ids, r)
			continue
		}
		bounds := t.merge(piece)
		for i := range bounds {
			end := len(piece)
			if i+1 < len(bounds) {
				end = bounds[i+1]
			}
			ids = append(ids, t.ranks[piece[bounds[i]:end]])
		}
	}
	return ids
}

// merge = byte-pair merge tiktoken (_byte_pair_merge): gabungkan pasangan ber-rank terendah
// berulang kali. Mengembalikan offset awal tiap token; O(n²), cukup untuk potongan pendek.
func (t *Tokenizer) merge(piece string) []int {
	starts := make([]int, len(piece))
	for i := range starts {
		starts[i] = i
	}
	rankOf := func(i int) (int, bool) { // rank gabungan token i dan i+1
		end := len(piece)
		if i+2 < len(starts) {
			end = starts[i+2]
		}
		r, ok := t.ranks[piece[starts[i]:end]]
		return r, ok
	}
	for len(starts) > 1 {
		best, bestRank := -1, 0
		for i := 0; i+1 < len(starts); i++ {
			if r, ok := rankOf(i); ok && (best < 0 || r < bestRank) {
				best, bestRank = i, r
			}
		}
		if best < 0 {
			break
		}
		starts = append(starts[:best+1], starts[best+2:]...)
	}
	return starts
}

// pieces memecah text sesuai pola pre-tokenisasi o200k_base.
func pieces(text string) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for pos := 0; pos < len(text); {
			n := 0
			if loc := pat.FindStringIndex(text[pos:]); loc != nil {
				n = loc[1]
			} else {
				n = splitWhitespace(text[pos:])
			}
			if !yield(text[pos : pos+n]) {
				return
			}
			pos += n
		}
	}
}

// splitWhitespace meniru `\s+(?!\S)|\s+` pada awal s: run whitespace maksimal bila sampai akhir
// teks; bila diikuti non-whitespace, run dikurangi satu rune (rune terakhir menjadi prefiks token
// berikutnya), kecuali run hanya satu rune.
func splitWhitespace(s string) int {
	end, runes, last := 0, 0, 0
	for end < len(s) {
		r, size := utf8.DecodeRuneInString(s[end:])
		if !isSpace(r) {
			break
		}
		last = end
		end += size
		runes++
	}
	switch {
	case runes == 0: // tidak seharusnya terjadi: ambil satu rune agar progres terjamin
		_, size := utf8.DecodeRuneInString(s)
		return size
	case end == len(s) || runes == 1:
		return end
	default:
		return last
	}
}

func isSpace(r rune) bool {
	switch {
	case r >= '\t' && r <= '\r', r == ' ', r == 0x85, r == 0xA0, r == 0x1680,
		r >= 0x2000 && r <= 0x200A, r == 0x2028, r == 0x2029, r == 0x202F, r == 0x205F, r == 0x3000:
		return true
	}
	return false
}
