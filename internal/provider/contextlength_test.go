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
		name    string
		message string
		want    int64
	}{
		{"maximum is", `maximum context length is 4097 tokens`, 4097},
		{"maximum colon", `maximum context length: 128000`, 128_000},
		{"maximum of", `maximum context length of 32768 tokens`, 32_768},
	}
	for _, c := range cases {
		e := &Error{Class: retry.ContextLength, Message: c.message}
		if got, ok := ContextWindow(e); !ok || got != c.want {
			t.Errorf("%s: ContextWindow = %d, %v; want %d", c.name, got, ok, c.want)
		}
		if got, ok := ContextWindow(fmt.Errorf("call: %w", e)); !ok || got != c.want {
			t.Errorf("%s wrapped: ContextWindow = %d, %v; want %d", c.name, got, ok, c.want)
		}
	}
	// Only the structured message is parsed; body echoes and other phrasing
	// (including the old loose "context window is") teach nothing.
	for _, e := range []*Error{
		{Class: retry.ContextLength, Body: []byte(`{"error":{"message":"echo: context window is 5000000"}}`)},
		{Class: retry.ContextLength, Message: `context window is 5000000`},
		{Class: retry.ContextLength, Message: `prompt is too long: 250000 tokens > 200000 maximum`},
		{Class: retry.ContextLength, Message: `context length of 32768`},
		{Class: retry.BadRequest, Message: `maximum context length is 4097 tokens`},
	} {
		if got, ok := ContextWindow(e); ok {
			t.Errorf("ContextWindow(%q/%q) = %d, want none", e.Message, e.Body, got)
		}
	}
	if _, ok := ContextWindow(errors.New("maximum context length is 4097 tokens")); ok {
		t.Error("a plain error taught a window")
	}
}
