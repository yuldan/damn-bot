// Package budget holds the usage ledger entry, cost accounting, and the
// single runtime token estimator (SPEC §4.5, GAP-COST-09, ADR-058/067/101).
// The o200k_base tokenizer exists only in the test harness (bench module).
package budget

import (
	"math"
	"unicode"
	"unicode/utf8"
)

// Chars per token by script (ADR-058): Latin (incl. ASCII, digits,
// punctuation, combining marks) 4; other alphabetic scripts (Cyrillic,
// Arabic, Devanagari, Greek, Hebrew, Thai, …) 2; CJK and everything outside
// the BMP (emoji) 1.
const (
	ratioLatin = 4
	ratioMid   = 2
	ratioWide  = 1
)

// Counts tallies characters per script class plus the image tokens of the
// request (ImageTokens per image, GAP-COST-09).
type Counts struct {
	Latin, Mid, Wide int
	Image            int64
}

// Count classifies every rune of s. Invalid UTF-8 bytes count as Wide (one
// token each, never under-estimated).
func Count(s string) Counts {
	var c Counts
	for i := 0; i < len(s); {
		b := s[i]
		if b < utf8.RuneSelf {
			c.Latin++
			i++
			continue
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		switch {
		case r == utf8.RuneError && n == 1, r > 0xFFFF, wide(r):
			c.Wide++
		case unicode.In(r, unicode.Latin, unicode.Common, unicode.Inherited):
			c.Latin++
		default:
			c.Mid++
		}
	}
	return c
}

// wide reports CJK ideographs, kana, Hangul, and CJK/fullwidth punctuation.
func wide(r rune) bool {
	return r >= 0x3000 && r <= 0x303F || r >= 0xFF00 && r <= 0xFFEF ||
		unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul)
}

// Add returns the sum of c and o.
func (c Counts) Add(o Counts) Counts {
	return Counts{Latin: c.Latin + o.Latin, Mid: c.Mid + o.Mid, Wide: c.Wide + o.Wide, Image: c.Image + o.Image}
}

// AddImage returns c plus one width × height image under formula f (the
// model's image_token_formula, ImageFormulas.For). A bad dimension or
// formula is ErrImage: the image is never counted as 0 tokens.
func (c Counts) AddImage(f ImageFormula, width, height int) (Counts, error) {
	n, err := ImageTokens(f, width, height)
	if err != nil {
		return c, err
	}
	c.Image += n
	return c, nil
}

// tokens is the uncalibrated token estimate of c. Image tokens join the
// text estimate before calibration, like every other part of the prompt
// that est_input_tokens covers.
func (c Counts) tokens() float64 {
	return float64(c.Latin)/ratioLatin + float64(c.Mid)/ratioMid + float64(c.Wide)/ratioWide + float64(c.Image)
}

// Raw is the uncalibrated estimate of c; it is what usage_ledger stores as
// est_input_tokens, so calibration compares the static ratios with real
// usage (ADR-101).
func (c Counts) Raw() int64 { return int64(math.Ceil(c.tokens())) }

// Calibration bounds (GAP-COST-09; lower bound ADR-101) and window.
const (
	CalibrationMin    = 0.5
	CalibrationMax    = 1.5
	CalibrationWindow = 50  // newest usage_ledger rows purpose='turn'
	calibrationAlpha  = 0.3 // EMA weight of the newest sample
	noCalibration     = 1.0 // factor without samples
)

// Sample is one usage_ledger row: real input_tokens and est_input_tokens.
type Sample struct {
	Real, Est int64
}

// Estimator is the per-session COST-09 estimator: the last real prompt
// size plus the calibrated estimate of what was added since. Not safe for
// concurrent use (one turn per session).
type Estimator struct {
	samples []Sample // oldest → newest, ≤ CalibrationWindow
	factor  float64
	anchor  int64 // prompt_tokens of the last call; 0 before the first
}

// NewEstimator derives the factor from the session's ledger samples
// (oldest → newest); the EMA is never persisted (ADR-058).
func NewEstimator(samples []Sample, lastPromptTokens int64) *Estimator {
	e := &Estimator{anchor: lastPromptTokens}
	for _, s := range samples {
		e.push(s)
	}
	e.factor = calibrate(e.samples)
	return e
}

// Observe records a finished call: real prompt tokens and the Raw estimate
// of the same request.
func (e *Estimator) Observe(real, raw int64) {
	e.anchor = real
	e.push(Sample{Real: real, Est: raw})
	e.factor = calibrate(e.samples)
}

func (e *Estimator) push(s Sample) {
	if s.Est <= 0 || s.Real <= 0 {
		return
	}
	if len(e.samples) == CalibrationWindow {
		e.samples = append(e.samples[:0], e.samples[1:]...)
	}
	e.samples = append(e.samples, s)
}

// Factor is the current calibration factor (1.0 without samples).
func (e *Estimator) Factor() float64 { return e.factor }

// Estimate returns the prompt size after adding delta to the last real
// prompt: anchor + ⌈delta × factor⌉.
func (e *Estimator) Estimate(delta Counts) int64 {
	return e.anchor + int64(math.Ceil(delta.tokens()*e.factor))
}

// calibrate is the EMA of real/est over samples, clamped to the bounds.
func calibrate(samples []Sample) float64 {
	if len(samples) == 0 {
		return noCalibration
	}
	f := float64(samples[0].Real) / float64(samples[0].Est)
	for _, s := range samples[1:] {
		f = calibrationAlpha*float64(s.Real)/float64(s.Est) + (1-calibrationAlpha)*f
	}
	return min(max(f, CalibrationMin), CalibrationMax)
}
