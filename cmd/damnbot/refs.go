package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/sandbox"
	"github.com/damnbot/damnbot/internal/tools"
)

// Reference expansion limits (MEM-024). maxRefBytes caps one payload in bytes
// on the read side; maxRefRunes is the same cap expressed in runes for the
// prompt copy, so truncation never tears a UTF-8 sequence (SEC17-8).
const (
	maxRefs     = 8
	maxRefBytes = 32 << 10
	maxRefRunes = 32 << 10
	maxRefTotal = 64 << 10
)

// expandRefs replaces @path, @folder/ and @url tokens with the referenced
// content wrapped in an <untrusted> boundary. Files go through the sandbox,
// URLs only when the run is online; every failure is surfaced, never silently
// dropped. expanded reports that at least one reference entered the prompt.
func expandRefs(ctx context.Context, prompt string, sb *sandbox.Sandbox, net *netx.Network, offline bool) (string, bool, error) {
	var b strings.Builder
	refs := 0
	total := 0
	for _, tok := range refTokens(prompt) {
		if !strings.HasPrefix(tok.text, "@") || len(tok.text) < 2 {
			b.WriteString(tok.text)
			b.WriteString(tok.sep)
			continue
		}
		refs++
		if refs > maxRefs {
			return "", false, fmt.Errorf("too many @ references (max %d)", maxRefs)
		}
		target := strings.TrimPrefix(tok.text, "@")
		var content string
		var err error
		if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
			content, err = fetchRefURL(ctx, net, offline, target)
		} else {
			content, err = readRefPath(ctx, sb, target)
		}
		if err != nil {
			return "", false, fmt.Errorf("@%s: %w", target, err)
		}
		// The readers already bound the payload in bytes; this second cap is
		// in runes so the prompt copy cannot end mid-sequence.
		if len(content) > maxRefRunes {
			if r := []rune(content); len(r) > maxRefRunes {
				content = string(r[:maxRefRunes]) + "\n[ref truncated at 32768 bytes]"
			}
		}
		total += len(content)
		if total > maxRefTotal {
			return "", false, fmt.Errorf("@ references exceed %d bytes in total", maxRefTotal)
		}
		fmt.Fprintf(&b, "<untrusted source=\"ref:%s\">\n%s\n</untrusted>",
			refSource(target), tools.EscapeUntrustedClose(content))
		b.WriteString(tok.sep)
	}
	return b.String(), refs > 0, nil
}

// expandPromptRefs expands @ references and taints the turn when any reference
// content was inlined. Reference content is untrusted input (ADR-070): without
// the taint, --unsafe-auto-shell would auto-approve non-read tools whose prompt
// carries file or network content (REVIEW-F2-SEC17-5). The taint is set before
// loop.Prompt, so it applies to every tool this turn evaluates.
func expandPromptRefs(ctx context.Context, disp *tools.Dispatcher, prompt string, sb *sandbox.Sandbox, net *netx.Network, offline bool) (string, error) {
	if !strings.Contains(prompt, "@") {
		return prompt, nil
	}
	expanded, refs, err := expandRefs(ctx, prompt, sb, net, offline)
	if err != nil {
		return "", err
	}
	if refs {
		disp.MarkTainted()
	}
	return expanded, nil
}

// refToken is one whitespace-delimited prompt chunk plus the whitespace that
// follows it, so expansion rewrites the prompt in place instead of replacing
// an equal-looking chunk elsewhere — which would land the next reference
// inside the block just inserted (SEC17-7).
type refToken struct{ text, sep string }

// refTokens splits a prompt like strings.Fields and keeps the separators.
func refTokens(s string) []refToken {
	var out []refToken
	for s != "" {
		i := strings.IndexFunc(s, unicode.IsSpace)
		if i < 0 {
			return append(out, refToken{text: s})
		}
		j := i
		for j < len(s) {
			r, size := utf8.DecodeRuneInString(s[j:])
			if !unicode.IsSpace(r) {
				break
			}
			j += size
		}
		out = append(out, refToken{text: s[:i], sep: s[i:j]})
		s = s[j:]
	}
	return out
}

// refSource bounds a reference name used inside the wrapper attribute: 120
// bytes, cut back to a rune boundary so the attribute stays valid UTF-8
// (SEC17-8).
func refSource(target string) string {
	target = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '<' || r == '>' {
			return -1
		}
		return r
	}, target)
	if len(target) > 120 {
		cut := 120
		for cut > 0 && !utf8.RuneStart(target[cut]) {
			cut--
		}
		target = target[:cut] + "…"
	}
	return target
}

// readRefPath reads one file or directory (non-recursive listing) inside a
// sandbox root.
func readRefPath(ctx context.Context, sb *sandbox.Sandbox, target string) (string, error) {
	if sb == nil {
		return "", fmt.Errorf("workspace sandbox unavailable")
	}
	verdict, resolved, err := sb.Check(ctx, target, sandbox.OpRead)
	if err != nil {
		return "", fmt.Errorf("sandbox check: %w", err)
	}
	if verdict != sandbox.Inside {
		return "", fmt.Errorf("path is outside the workspace")
	}
	f, _, err := sb.OpenResolved(ctx, resolved, false)
	if err != nil {
		return "", fmt.Errorf("open reference: %w", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			// The content is already read; a close error cannot be reported
			// through the reference value.
			return
		}
	}()
	fi, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat reference: %w", err)
	}
	if fi.IsDir() {
		names, rerr := f.Readdirnames(201)
		if rerr != nil && rerr != io.EOF {
			return "", fmt.Errorf("read directory: %w", rerr)
		}
		truncated := len(names) > 200
		if truncated {
			names = names[:200]
		}
		var b strings.Builder
		fmt.Fprintf(&b, "directory %s (%d entries)", filepath.ToSlash(target), len(names))
		if truncated {
			b.WriteString(", listing truncated")
		}
		for _, n := range names {
			b.WriteString("\n- ")
			b.WriteString(n)
		}
		return b.String(), nil
	}
	b, err := io.ReadAll(io.LimitReader(f, maxRefBytes+1))
	if err != nil {
		return "", fmt.Errorf("read reference: %w", err)
	}
	if len(b) == 0 {
		return "(empty file)", nil
	}
	return string(b), nil
}

// fetchRefURL fetches a text reference through the egress-guarded client.
func fetchRefURL(ctx context.Context, net *netx.Network, offline bool, url string) (string, error) {
	if offline {
		return "", fmt.Errorf("URL references need network access (--offline)")
	}
	if net == nil {
		return "", fmt.Errorf("network unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("reference request: %w", err)
	}
	resp, err := net.Client("refs", netx.Tool).DoOK(req)
	if err != nil {
		return "", fmt.Errorf("fetch reference: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			// The body is already read; close errors are not actionable here.
			return
		}
	}()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxRefBytes+1))
	if err != nil {
		return "", fmt.Errorf("read reference response: %w", err)
	}
	if len(b) > maxRefBytes {
		return "", fmt.Errorf("URL body exceeds %d bytes", maxRefBytes)
	}
	return string(b), nil
}

// applyPromptRefs expands @ references into o.prompt and taints the turn when
// any reference content was inlined (MEM-024, ADR-070); it is the production
// call site of expandPromptRefs.
func applyPromptRefs(ctx context.Context, disp *tools.Dispatcher, o *runOptions, model *runModel) error {
	expanded, err := expandPromptRefs(ctx, disp, o.prompt, model.sb, model.net, o.offline)
	if err != nil {
		return fmt.Errorf("context refs: %w", err)
	}
	o.prompt = expanded
	return nil
}
