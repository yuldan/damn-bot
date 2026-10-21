package provider

import (
	"errors"
	"fmt"
	"testing"

	"github.com/damnbot/damnbot/internal/netx/retry"
)

func TestContextWindow(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		want int64
	}{
		{"openai maximum", `{"error":{"message":"This model's maximum context length is 4097 tokens.","code":"context_length_exceeded"}}`, 4097},
		{"maximum colon", `{"error":{"message":"maximum context length: 128000"}}`, 128_000},
		{"context length of", `{"error":{"message":"a context length of 32768 is allowed"}}`, 32_768},
		{"context window", `{"error":{"message":"the context window is 200000 tokens"}}`, 200_000},
		{"prompt too long", `{"error":{"message":"prompt is too long: 250000 tokens > 200000 maximum"}}`, 200_000},
	}
	for _, c := range cases {
		e := &Error{Class: retry.ContextLength, Body: []byte(c.body)}
		if got, ok := ContextWindow(e); !ok || got != c.want {
			t.Errorf("%s: ContextWindow = %d, %v; want %d", c.name, got, ok, c.want)
		}
		if got, ok := ContextWindow(fmt.Errorf("call: %w", e)); !ok || got != c.want {
			t.Errorf("%s wrapped: ContextWindow = %d, %v; want %d", c.name, got, ok, c.want)
		}
	}
	if _, ok := ContextWindow(&Error{Class: retry.BadRequest, Body: []byte(`{"error":{"message":"maximum context length is 4097 tokens"}}`)}); ok {
		t.Error("a non-context_length error taught a window")
	}
	if _, ok := ContextWindow(&Error{Class: retry.ContextLength, Body: []byte(`{"error":{"message":"too long"}}`)}); ok {
		t.Error("an unrecognized message guessed a window")
	}
	if _, ok := ContextWindow(errors.New("maximum context length is 4097 tokens")); ok {
		t.Error("a plain error taught a window")
	}
}
