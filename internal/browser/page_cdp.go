package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// cdpMethodPattern is the shape of a protocol method name.
var cdpMethodPattern = regexp.MustCompile(`^[A-Z][A-Za-z]*\.[a-z][A-Za-z]*$`)

// cdpSafeMethods is the reviewed read-only allowlist of browser_cdp
// (TOOL-017): no JavaScript execution, no cookies/storage, no navigation, no
// debugger, fetch interception or target control.
var cdpSafeMethods = map[string]bool{
	"Page.getLayoutMetrics":     true,
	"Page.getNavigationHistory": true,
	"Page.getFrameTree":         true,
	"DOM.getDocument":           true,
	"DOM.describeNode":          true,
	"DOM.getBoxModel":           true,
}

// cdpSensitiveMethods is the reviewed allowlist of methods that reach
// credentials, page script or the browser itself. Each one needs the same
// per-call approval the tool always asks for; none is reachable with a session
// approval.
var cdpSensitiveMethods = map[string]bool{
	"Network.getCookies":          true,
	"Network.getAllCookies":       true,
	"Network.setCookie":           true,
	"Network.setCookies":          true,
	"Network.deleteCookies":       true,
	"Network.clearBrowserCookies": true,
	"Runtime.evaluate":            true,
	"Runtime.callFunctionOn":      true,
	"Runtime.compileScript":       true,
	"Runtime.runScript":           true,
	"DOM.setFileInputFiles":       true,
}

// cdpSensitivePrefixes are whole domains that own credentials, page script,
// interception or the browser target.
var cdpSensitivePrefixes = []string{"Storage.", "Debugger.", "Fetch.", "Target.", "Browser."}

// cdpMethodClass reports whether method is on the reviewed allowlist and
// whether it is sensitive. A method outside the allowlist is refused: raw CDP
// is not a generic escape hatch around the page policy.
func cdpMethodClass(method string) (sensitive, allowed bool) {
	if !cdpMethodPattern.MatchString(method) {
		return false, false
	}
	switch {
	case cdpSafeMethods[method]:
		return false, true
	case cdpSensitiveMethods[method]:
		return true, true
	case strings.HasPrefix(method, "Network.") && strings.Contains(method, "Cookie"):
		return true, true
	}
	for _, prefix := range cdpSensitivePrefixes {
		if strings.HasPrefix(method, prefix) {
			return true, true
		}
	}
	return false, false
}

// cdpParams is one raw protocol call's parameters.
type cdpParams struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// RawCDP sends one reviewed protocol method and returns its JSON result capped
// at MaxResult. A `file:` URL in the parameters makes the call sensitive: it
// reaches the local disk and can never be approved by a session approval.
func (p *Page) RawCDP(ctx context.Context, method string, params json.RawMessage) (string, error) {
	if p.conn == nil {
		return "", fmt.Errorf("%w: cdp", errUnsupported)
	}
	if _, allowed := cdpMethodClass(method); !allowed {
		return "", fmt.Errorf("%w: %s is outside the reviewed CDP allowlist", errDenied, method)
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

// cdpSensitive reports whether a call needs the sensitive treatment: a
// sensitive method, or a parameter carrying a local file URL.
func cdpSensitive(method string, params json.RawMessage) bool {
	if sensitive, _ := cdpMethodClass(method); sensitive {
		return true
	}
	value, ok := decodeParams(params)
	if !ok {
		return false
	}
	return containsFileURL(value, 0)
}

// decodeParams decodes the raw parameter object, reporting whether it is a
// valid JSON object cheaply.
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

// containsFileURL walks a decoded parameter value for a `file:` URL. The walk
// is bounded so a hostile parameter cannot turn the check into a traversal.
func containsFileURL(value any, depth int) bool {
	if depth > 8 {
		return false
	}
	switch v := value.(type) {
	case string:
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(v)), "file:")
	case []any:
		for _, item := range v {
			if containsFileURL(item, depth+1) {
				return true
			}
		}
	case map[string]any:
		for _, item := range v {
			if containsFileURL(item, depth+1) {
				return true
			}
		}
	}
	return false
}
