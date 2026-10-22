package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/sandbox"
	"github.com/damnbot/damnbot/internal/tools"
)

// Reference expansion limits (MEM-024).
const (
	maxRefs     = 8
	maxRefBytes = 32 << 10
	maxRefTotal = 64 << 10
)

// expandRefs replaces @path, @folder/ and @url tokens with the referenced
// content wrapped in an <untrusted> boundary. Files go through the sandbox,
// URLs only when the run is online; every failure is surfaced, never silently
// dropped.
func expandRefs(ctx context.Context, prompt string, sb *sandbox.Sandbox, net *netx.Network, offline bool) (string, error) {
	fields := strings.Fields(prompt)
	refs := 0
	total := 0
	for _, f := range fields {
		if !strings.HasPrefix(f, "@") || len(f) < 2 {
			continue
		}
		refs++
		if refs > maxRefs {
			return "", fmt.Errorf("too many @ references (max %d)", maxRefs)
		}
		target := strings.TrimPrefix(f, "@")
		var content string
		var err error
		if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
			content, err = fetchRefURL(ctx, net, offline, target)
		} else {
			content, err = readRefPath(ctx, sb, target)
		}
		if err != nil {
			return "", fmt.Errorf("@%s: %w", target, err)
		}
		r := []rune(content)
		if len(r) > maxRefBytes {
			r = r[:maxRefBytes]
			content = string(r) + "\n[ref truncated at 32768 bytes]"
		}
		total += len(content)
		if total > maxRefTotal {
			return "", fmt.Errorf("@ references exceed %d bytes in total", maxRefTotal)
		}
		expanded := fmt.Sprintf("<untrusted source=\"ref:%s\">\n%s\n</untrusted>",
			refSource(target), tools.EscapeUntrustedClose(content))
		prompt = strings.Replace(prompt, f, expanded, 1)
	}
	return prompt, nil
}

// refSource bounds a reference name used inside the wrapper attribute.
func refSource(target string) string {
	target = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '"' || r == '<' || r == '>' {
			return -1
		}
		return r
	}, target)
	if len(target) > 120 {
		target = target[:120] + "…"
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
