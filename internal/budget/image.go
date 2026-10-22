package budget

import (
	"errors"
	"fmt"
	"math"
)

// ImageFormula selects the per-model image token preset (GAP-COST-09, F2).
// The provider registry names a formula; this package implements the four
// presets that cover every route in the F1 registry and a conservative area
// fallback for an unknown (not text-only) model. Dimensions are supplied by
// the caller: the estimator never decodes an image, so the package keeps its
// zero dependencies (ADR-058/067).
type ImageFormula string

// Image formulas. The strings are stable identifiers suitable for
// providers.toml's image_token_formula.
const (
	// ImageOpenAI is the high-detail tile formula of OpenAI vision models:
	// fit inside 2048², scale the shortest side to 768, then
	// 85 + 170 × ceil(w/512) × ceil(h/512).
	ImageOpenAI ImageFormula = "openai_tiles"
	// ImageAnthropic is Claude's area estimate: ceil(w × h / 750).
	ImageAnthropic ImageFormula = "anthropic_area"
	// ImageGemini is the Gemini tile formula: 258 tokens for an image
	// inside 384², otherwise 258 per 768² tile.
	ImageGemini ImageFormula = "gemini_tiles"
	// ImageGeneric is the conservative fallback of an unknown model:
	// the Claude area estimate (never the smallest formula).
	ImageGeneric ImageFormula = "area_750"
)

// Image formula constants. MaxImageDim rejects absurd or corrupt dimensions
// before any arithmetic; the tile counts stay far below int64 overflow even
// at that bound.
const (
	MaxImageDim     = 1 << 20 // 1,048,576 px per side
	openAITile      = 512
	openAIMaxSide   = 2048
	openAIMinSide   = 768
	openAIBase      = 85
	openAITileToken = 170
	geminiSmallSide = 384
	geminiTile      = 768
	geminiTileToken = 258
	areaDivisor     = 750
)

// ErrImage rejects an unusable image dimension or formula. Callers treat it
// as a fail-closed accounting error, never as zero tokens.
var ErrImage = errors.New("budget: invalid image")

// ImageTokens returns the estimated prompt tokens of one image of
// width × height for formula f. It is deterministic and allocation-free.
func ImageTokens(f ImageFormula, width, height int) (int64, error) {
	if width <= 0 || height <= 0 || width > MaxImageDim || height > MaxImageDim {
		return 0, fmt.Errorf("%w: dimensions %dx%d", ErrImage, width, height)
	}
	switch f {
	case ImageOpenAI:
		w, h := fitSide(width, height, openAIMaxSide)
		w, h = growSide(w, h, openAIMinSide)
		tiles := int64((w + openAITile - 1) / openAITile * ((h + openAITile - 1) / openAITile))
		return openAIBase + openAITileToken*tiles, nil
	case ImageGemini:
		if width <= geminiSmallSide && height <= geminiSmallSide {
			return geminiTileToken, nil
		}
		tiles := int64((width+geminiTile-1)/geminiTile) * int64((height+geminiTile-1)/geminiTile)
		return geminiTileToken * tiles, nil
	case ImageAnthropic, ImageGeneric, "":
		return areaTokens(width, height), nil
	default:
		return 0, fmt.Errorf("%w: formula %q", ErrImage, f)
	}
}

// ParseImageFormula maps a registry string to a preset. ok is false for an
// unknown non-empty value; "" selects ImageGeneric.
func ParseImageFormula(s string) (ImageFormula, bool) {
	switch f := ImageFormula(s); f {
	case "", ImageGeneric, ImageAnthropic, ImageOpenAI, ImageGemini:
		return f, true
	default:
		return "", false
	}
}

// ImageFormulas maps "provider/model" refs to their declared
// image_token_formula (registry entry or config override, GAP-COST-09). A
// model without an entry gets ImageGeneric: model ids are not guessed by
// family, because members of one family use different formulas (a wrong
// guess under-estimates). Immutable after NewImageFormulas.
type ImageFormulas struct{ byRef map[string]ImageFormula }

// NewImageFormulas validates declared (ref → formula name). An unknown name
// is rejected, so a typo never silently falls back to another preset; an
// empty name means "not declared".
func NewImageFormulas(declared map[string]string) (ImageFormulas, error) {
	m := make(map[string]ImageFormula, len(declared))
	for ref, name := range declared {
		f, ok := ParseImageFormula(name)
		if !ok {
			return ImageFormulas{}, fmt.Errorf("%w: %s image_token_formula %q", ErrImage, ref, name)
		}
		if f != "" {
			m[ref] = f
		}
	}
	return ImageFormulas{byRef: m}, nil
}

// For returns the formula of ref ("provider/model"); ImageGeneric when the
// model declares none. The result feeds Counts.AddImage.
func (f ImageFormulas) For(ref string) ImageFormula {
	if v, ok := f.byRef[ref]; ok {
		return v
	}
	return ImageGeneric
}

// areaTokens is ceil(w × h / 750); w and h are already bounded by
// MaxImageDim, so the product fits in int64.
func areaTokens(w, h int) int64 {
	area := int64(w) * int64(h)
	return (area + areaDivisor - 1) / areaDivisor
}

// fitSide scales (w,h) down to fit a max × max square, preserving aspect
// ratio with ceil rounding (never under-estimate a fractional pixel).
func fitSide(w, h, max int) (int, int) {
	if w <= max && h <= max {
		return w, h
	}
	if w >= h {
		return max, int(math.Ceil(float64(h) * float64(max) / float64(w)))
	}
	return int(math.Ceil(float64(w) * float64(max) / float64(h))), max
}

// growSide scales the shortest side up to min; the long side follows and may
// exceed max, exactly like the OpenAI high-detail pipeline.
func growSide(w, h, min int) (int, int) {
	if w < h {
		if w < min {
			return min, int(math.Ceil(float64(h) * float64(min) / float64(w)))
		}
		return w, h
	}
	if h < min {
		return int(math.Ceil(float64(w) * float64(min) / float64(h))), min
	}
	return w, h
}
