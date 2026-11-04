//go:build !minimal

package browser

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// maxCDPParams bounds one raw parameter object before it reaches the transport.
const maxCDPParams = 64 << 10

// rawCDP sends one reviewed protocol method (TOOL-017). The tool is non-read:
// every call raises one allow-once approval whose preview names the method and
// the number of parameter fields, never a parameter value or page content, and
// a session approval can never widen it. Methods outside the reviewed
// allowlist are refused before approval.
func (m *Module) rawCDP(ctx context.Context, raw json.RawMessage, env host.Env) (host.Result, error) {
	var a cdpParams
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, invalidArgs(err)
	}
	if _, allowed := cdpMethodClass(a.Method); !allowed {
		return host.Result{}, &host.Error{
			Code: "denied", Message: "browser_cdp: " + a.Method + " is outside the reviewed CDP allowlist",
			Hint: "use the dedicated browser tools, or a method from the reviewed allowlist",
		}
	}
	if len(a.Params) > maxCDPParams {
		return host.Result{}, &host.Error{Code: "invalid_args", Message: "browser_cdp: parameters too large"}
	}
	sensitive := cdpSensitive(a.Method, a.Params)
	preview := fmt.Sprintf("browser_cdp %s (%s) with %d parameter field(s)",
		a.Method, cdpClassLabel(sensitive), paramFields(a.Params))
	if err := m.approve(ctx, "browser_cdp", "browser:cdp", preview, env, raw); err != nil {
		return host.Result{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.open(ctx)
	if err != nil {
		return host.Result{}, pageError(err)
	}
	value, err := p.RawCDP(ctx, a.Method, a.Params)
	if err != nil {
		return host.Result{}, pageError(err)
	}
	return host.Result{
		Content: value, Meta: map[string]any{"method": a.Method, "sensitive": sensitive},
		UntrustedSource: m.taint(),
	}, nil
}

// cdpClassLabel names the risk class in the approval preview so the operator
// sees a sensitive call before it runs.
func cdpClassLabel(sensitive bool) string {
	if sensitive {
		return "sensitive"
	}
	return "read-only"
}

// paramFields counts the top-level fields of a parameter object without
// exposing any value.
func paramFields(params json.RawMessage) int {
	fields, ok := decodeParams(params)
	if !ok {
		return 0
	}
	object, ok := fields.(map[string]any)
	if !ok {
		return 0
	}
	return len(object)
}
