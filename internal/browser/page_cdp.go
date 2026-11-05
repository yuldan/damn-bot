package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// cdpMethodPattern is the shape of a protocol method name.
var cdpMethodPattern = regexp.MustCompile(`^[A-Z][A-Za-z]*\.[a-z][A-Za-z]*$`)

// cdpReviewedSafe is the reviewed read-only allowlist of browser_cdp
// (TOOL-017): no JavaScript execution, no cookies/storage, no navigation, no
// debugger, fetch interception or target control. It is a function so the
// reviewed set stays a closed literal the test walks, without a package global
// the slice never escapes from.
func cdpReviewedSafe() []string {
	return []string{
		"Page.getLayoutMetrics", "Page.getNavigationHistory", "Page.getFrameTree",
		"DOM.getDocument", "DOM.describeNode", "DOM.getBoxModel",
	}
}

// cdpReviewedSensitive names the reviewed methods that reach credentials, page
// script or the browser itself. Each one needs the same per-call approval the
// tool always asks for; none is reachable with a session approval.
func cdpReviewedSensitive() []string {
	return []string{
		"Network.getCookies", "Network.getAllCookies", "Network.setCookie",
		"Network.setCookies", "Network.deleteCookies", "Network.clearBrowserCookies",
		"Runtime.evaluate", "Runtime.callFunctionOn", "Runtime.compileScript",
		"Runtime.runScript", "DOM.setFileInputFiles",
	}
}

// cdpReviewedDomains are whole domains that own credentials, page script,
// interception or the browser target; every method below them is sensitive.
func cdpReviewedDomains() []string {
	return []string{"Storage", "Debugger", "Fetch", "Target", "Browser"}
}

// cdpSafe reports whether method is on the reviewed read-only allowlist.
func cdpSafe(method string) bool { return slices.Contains(cdpReviewedSafe(), method) }

// cdpSensitiveMethod reports whether method is on the reviewed sensitive list.
func cdpSensitiveMethod(method string) bool { return slices.Contains(cdpReviewedSensitive(), method) }

// cdpSensitiveDomain reports whether method belongs to a sensitive domain.
func cdpSensitiveDomain(method string) bool {
	domain, _, ok := strings.Cut(method, ".")
	return ok && slices.Contains(cdpReviewedDomains(), domain)
}

// cdpMethodClass reports whether method is on the reviewed allowlist and
// whether it is sensitive. A method outside the allowlist is refused: raw CDP
// is not a generic escape hatch around the page policy.
func cdpMethodClass(method string) (sensitive, allowed bool) {
	switch {
	case !cdpMethodPattern.MatchString(method):
		return false, false
	case cdpSafe(method):
		return false, true
	case cdpSensitiveMethod(method), cdpSensitiveDomain(method):
		return true, true
	case strings.HasPrefix(method, "Network.") && strings.Contains(method, "Cookie"):
		return true, true
	}
	return false, false
}

// cdpParams is one raw protocol call's parameters.
type cdpParams struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// RawCDP sends one reviewed protocol method and returns its JSON result capped
// at MaxResult. Parameters the reviewed policy refuses are rejected here too,
// so the guard holds for every caller and not only the tool.
func (p *Page) RawCDP(ctx context.Context, method string, params json.RawMessage) (string, error) {
	if p.conn == nil {
		return "", fmt.Errorf("%w: cdp", errUnsupported)
	}
	if _, allowed := cdpMethodClass(method); !allowed {
		return "", fmt.Errorf("%w: %s is outside the reviewed CDP allowlist", errDenied, method)
	}
	if cdpPayloadDenied(method, params) {
		return "", fmt.Errorf("%w: %s parameters are refused by the CDP policy", errDenied, method)
	}
	var decoded any
	if len(params) > 0 {
		if err := json.Unmarshal(params, &decoded); err != nil {
			return "", fmt.Errorf("%w: cdp params: %w", errBadConfig, err)
		}
	}
	raw, err := p.conn.Call(ctx, method, decoded)
	if err != nil {
		return "", fmt.Errorf("browser: cdp: %w", err)
	}
	if len(raw) > p.cfg.MaxResult {
		return "", fmt.Errorf("%w: cdp result %d > %d bytes", errTooLarge, len(raw), p.cfg.MaxResult)
	}
	if len(raw) == 0 {
		return "{}", nil
	}
	return string(raw), nil
}

// cdpPayloadDenied reports whether params carry a payload the reviewed policy
// refuses regardless of approval: a script or data URL, a local file reference
// outside the upload method, a traversal in an upload path, or a private or
// metadata target URL. It runs before approval, so the operator is never asked
// to approve a payload the tool would refuse anyway.
func cdpPayloadDenied(method string, params json.RawMessage) bool {
	value, ok := decodeParams(params)
	if !ok {
		return false
	}
	if cdpUnsafeScheme(value) {
		return true
	}
	switch method {
	case "DOM.setFileInputFiles":
		return cdpUploadPathsDenied(value)
	case "Target.createTarget":
		return cdpTargetDenied(value)
	}
	return cdpForeignFileParam(value)
}

// cdpUnsafeScheme reports whether any parameter string is a script or data
// URL. Page content is untrusted input, never a protocol parameter.
func cdpUnsafeScheme(value any) bool {
	return cdpWalkStrings(value, 0, func(s string) bool {
		t := strings.ToLower(strings.TrimSpace(s))
		return strings.HasPrefix(t, "data:") || strings.HasPrefix(t, "javascript:")
	})
}

// cdpForeignFileParam reports whether any parameter string is a `file:` URL
// outside DOM.setFileInputFiles, the only reviewed method that reads local
// files.
func cdpForeignFileParam(value any) bool {
	return cdpWalkStrings(value, 0, func(s string) bool {
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "file:")
	})
}

// cdpUploadPathsDenied reports whether an upload path carries a traversal
// segment, raw or percent-encoded.
func cdpUploadPathsDenied(value any) bool {
	return cdpWalkStrings(value, 0, func(s string) bool {
		raw := strings.TrimSpace(s)
		if cdpTraversalSegment(raw) {
			return true
		}
		unescaped, err := url.PathUnescape(raw)
		return err == nil && unescaped != raw && cdpTraversalSegment(unescaped)
	})
}

// cdpTraversalSegment reports whether path contains a `..` segment.
func cdpTraversalSegment(path string) bool {
	return slices.Contains(strings.Split(strings.ReplaceAll(path, `\`, "/"), "/"), "..")
}

// cdpTargetDenied reports whether Target.createTarget names a destination the
// page policy would refuse: a non-http(s) scheme, credentials in the URL, a
// loopback/private/link-local host or an internal DNS name.
func cdpTargetDenied(value any) bool {
	object, ok := value.(map[string]any)
	if !ok {
		return false
	}
	raw, ok := object["url"].(string)
	if !ok {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return true
	}
	host := strings.ToLower(u.Hostname())
	return localHost(host) || host == "metadata.google.internal" || strings.HasSuffix(host, ".internal")
}

// decodeParams decodes the raw parameter object, reporting whether it is a
// valid JSON value.
func decodeParams(params json.RawMessage) (any, bool) {
	if len(params) == 0 {
		return nil, false
	}
	var value any
	if err := json.Unmarshal(params, &value); err != nil {
		return nil, false
	}
	return value, true
}

// cdpWalkStrings calls fn for every string in a decoded parameter value,
// bounded so a hostile parameter cannot turn the check into a deep traversal.
func cdpWalkStrings(value any, depth int, fn func(string) bool) bool {
	if depth > 8 {
		return false
	}
	switch v := value.(type) {
	case string:
		return fn(v)
	case []any:
		for _, item := range v {
			if cdpWalkStrings(item, depth+1, fn) {
				return true
			}
		}
	case map[string]any:
		for _, item := range v {
			if cdpWalkStrings(item, depth+1, fn) {
				return true
			}
		}
	}
	return false
}
