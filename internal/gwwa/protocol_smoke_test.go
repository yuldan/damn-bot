package gwwa

import (
	"errors"
	"testing"
	"time"
)

// TestF7GWWABridgeProtocol is the named contract test of this slice: the
// handshake must enforce the pinned protocol major and the build-time
// wa_version, and the bridge's cold-outbound refusal must be recognisable from
// both the message text and the structured error data.
func TestF7GWWABridgeProtocol(t *testing.T) {
	t.Parallel()
	checkHello(t)
	checkColdOutbound(t)
	checkGuardWindow(t)
	checkVerdicts(t)
}

func checkHello(t *testing.T) {
	t.Helper()
	good := Hello{Protocol: Protocol, BridgeVersion: "1.2.3", Capabilities: Capabilities{WaVersion: "2.3000.0", WaVersionSource: ProtocolSourceBuild}}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid hello rejected: %v", err)
	}
	for _, bad := range []Hello{
		{Protocol: "other", Capabilities: Capabilities{WaVersion: "2.3000.0", WaVersionSource: ProtocolSourceBuild}},
		{Protocol: Protocol},
		{Protocol: Protocol, Capabilities: Capabilities{WaVersion: " ", WaVersionSource: ProtocolSourceBuild}},
	} {
		if err := bad.Validate(); !errors.Is(err, ErrProtocol) {
			t.Errorf("hello %+v err = %v, want ErrProtocol", bad, err)
		}
	}
	if err := (Hello{Protocol: Protocol + ".9", Capabilities: Capabilities{WaVersion: "2.3000.0", WaVersionSource: ProtocolSourceBuild}}).Validate(); err != nil {
		t.Errorf("dotted protocol rejected: %v", err)
	}
}

func checkColdOutbound(t *testing.T) {
	t.Helper()
	text := &RPCError{Code: 409, Message: "cold_outbound_denied: no inbound history"}
	if !text.IsColdOutbound() {
		t.Error("message-based cold outbound not detected")
	}
	data := &RPCError{Code: 409, Message: "denied", Data: []byte(`{"code":"cold_outbound_denied"}`)}
	if !data.IsColdOutbound() {
		t.Error("data-based cold outbound not detected")
	}
	if (&RPCError{Code: 500, Message: "boom"}).IsColdOutbound() {
		t.Error("plain bridge error reported as cold outbound")
	}
	if got := text.Error(); got == "" {
		t.Error("empty error text")
	}
}

func checkGuardWindow(t *testing.T) {
	t.Helper()
	g := NewGuard(2)
	if g.Allow("628123") {
		t.Error("guard allowed a chat without inbound history")
	}
	g.Note("628123", time.Unix(1_700_000_000, 0))
	g.Note("628456", time.Unix(1_700_000_001, 0))
	if !g.Allow("628123") {
		t.Error("guard lost a noted chat")
	}
	g.Note("628789", time.Unix(1_700_000_002, 0))
	if g.Allow("628123") {
		t.Error("guard did not evict the oldest chat past its window")
	}
}

func checkVerdicts(t *testing.T) {
	t.Helper()
	if v := Evaluate(Health{Reason: ReasonBanned}); v.Action != ActionNeedsAttention || v.Instruction == "" {
		t.Errorf("banned verdict = %+v", v)
	}
	if v := Evaluate(Health{Reason: ReasonTemporaryBan}); v.RetryAfter == 0 || v.Action != ActionNeedsAttention {
		t.Errorf("temporary ban verdict = %+v", v)
	}
}
