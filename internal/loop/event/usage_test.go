package event

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"
)

// TestUsageJSONIgnoresTTFT locks the event contract: TTFT is the measured
// telemetry behind usage_ledger.ttft_ms (SPEC §11.2), not a field of a
// persisted event payload (the llm_call payload carries tokens and cost only)
// or of the turn_result usage object (SPEC §7.2). Adding it for the ledger
// must not change a single byte of the JSON any frontend or golden sees.
func TestUsageJSONIgnoresTTFT(t *testing.T) {
	t.Parallel()
	cost := 0.0412
	before := Usage{Input: 41_230, CachedInput: 30_110, Output: 2_210, CostUSD: &cost, Iterations: 7}
	measured := before
	measured.TTFT = 850 * time.Millisecond

	want, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(measured)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("TTFT changed the serialized usage:\n got %s\nwant %s", got, want)
	}
	if bytes.Contains(got, []byte("TTFT")) {
		t.Fatalf("serialized usage leaks TTFT: %s", got)
	}
}
