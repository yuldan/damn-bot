package budget

import (
	"errors"
	"math"
	"testing"
)

func TestImageTokens(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		f    ImageFormula
		w, h int
		want int64
	}{
		// OpenAI high-detail: fit 2048², grow the short side to 768, 512 tiles.
		{"openai 1x1", ImageOpenAI, 1, 1, 765},
		{"openai 512x512", ImageOpenAI, 512, 512, 765},
		{"openai 768x768", ImageOpenAI, 768, 768, 765},
		{"openai 1024x1024", ImageOpenAI, 1024, 1024, 765},
		{"openai 1025x1024", ImageOpenAI, 1025, 1024, 1105},
		{"openai 2048x2048", ImageOpenAI, 2048, 2048, 2805},
		{"openai 2049x2048", ImageOpenAI, 2049, 2048, 2805},
		{"openai 100x10000", ImageOpenAI, 100, 10000, 50065},
		{"openai 10000x100", ImageOpenAI, 10000, 100, 50065},
		{"openai max dim", ImageOpenAI, MaxImageDim, 1, 1_044_565},
		// Anthropic/generic area: ceil(w × h / 750).
		{"anthropic 1x1", ImageAnthropic, 1, 1, 1},
		{"anthropic 750x1", ImageAnthropic, 750, 1, 1},
		{"anthropic 751x1", ImageAnthropic, 751, 1, 2},
		{"anthropic 1000x1000", ImageAnthropic, 1000, 1000, 1334},
		{"anthropic 2048x2048", ImageAnthropic, 2048, 2048, 5593},
		{"generic 1000x1000", ImageGeneric, 1000, 1000, 1334},
		{"empty formula", "", 1000, 1000, 1334},
		// Gemini: 258 tokens inside 384², else 258 per 768² tile.
		{"gemini 384x384", ImageGemini, 384, 384, 258},
		{"gemini 1x1", ImageGemini, 1, 1, 258},
		{"gemini 385x385", ImageGemini, 385, 385, 258},
		{"gemini 768x768", ImageGemini, 768, 768, 258},
		{"gemini 769x768", ImageGemini, 769, 768, 516},
		{"gemini 769x769", ImageGemini, 769, 769, 1032},
		{"gemini 2048x2048", ImageGemini, 2048, 2048, 2322},
	}
	for _, c := range cases {
		got, err := ImageTokens(c.f, c.w, c.h)
		if err != nil || got != c.want {
			t.Errorf("%s: ImageTokens(%q,%d,%d) = %d, %v; want %d", c.name, c.f, c.w, c.h, got, err, c.want)
		}
	}
}

func TestImageTokensRejects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		f    ImageFormula
		w, h int
	}{
		{"zero width", ImageOpenAI, 0, 1},
		{"negative height", ImageAnthropic, 1, -1},
		{"too wide", ImageGemini, MaxImageDim + 1, 1},
		{"too tall", ImageGeneric, math.MaxInt, 1},
		{"unknown formula", ImageFormula("nope"), 10, 10},
	}
	for _, c := range cases {
		if _, err := ImageTokens(c.f, c.w, c.h); !errors.Is(err, ErrImage) {
			t.Errorf("%s: err = %v, want ErrImage", c.name, err)
		}
	}
}

func TestParseImageFormula(t *testing.T) {
	t.Parallel()
	for _, f := range []ImageFormula{"", ImageGeneric, ImageAnthropic, ImageOpenAI, ImageGemini} {
		got, ok := ParseImageFormula(string(f))
		if !ok || got != f {
			t.Errorf("ParseImageFormula(%q) = %q %v", f, got, ok)
		}
	}
	if _, ok := ParseImageFormula("openai"); ok {
		t.Error("unknown formula accepted")
	}
}

func BenchmarkImageTokens(b *testing.B) {
	for _, f := range []ImageFormula{ImageOpenAI, ImageAnthropic, ImageGemini} {
		b.Run(string(f), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := ImageTokens(f, 1024, 768); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
