//go:build windows

package hooks

import (
	"context"
	"testing"
)

// TestLookupAppendsExe: Windows users and fixtures name the logical hook
// without the extension; Lookup/Run must resolve <name>.exe.
func TestLookupAppendsExe(t *testing.T) {
	reg, dir := testRegistry(t)
	hookFile(t, dir, "allow")
	h, err := reg.Lookup("allow")
	if err != nil || h.Problem != "" {
		t.Fatalf("Lookup(allow) = %+v, %v; want the .exe fixture", h, err)
	}
	res, err := reg.Run(context.Background(), "allow", EventGatewayInbound, map[string]any{
		"platform": "telegram", "chat_id": "1", "user_id": "2", "text": "hi",
	})
	if err != nil || res.Decision != DecisionAllow {
		t.Fatalf("Run(allow) = %+v, %v; want allow", res, err)
	}
}

// TestLookupExactExe keeps the fully qualified name working.
func TestLookupExactExe(t *testing.T) {
	reg, dir := testRegistry(t)
	hookFile(t, dir, "allow")
	if h, err := reg.Lookup("allow.exe"); err != nil || h.Problem != "" {
		t.Fatalf("Lookup(allow.exe) = %+v, %v", h, err)
	}
}
