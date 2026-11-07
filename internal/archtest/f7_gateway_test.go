package archtest

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
)

// TestF7NoInlineSleep is the GAP-GW-03 negative control: no platform adapter
// or gateway core file may pace itself with time.Sleep. Pacing belongs to
// gateway/outq; forbidigo enforces the same rule, this test proves it on the
// tree the PR ships.
func TestF7NoInlineSleep(t *testing.T) {
	t.Parallel()
	var hits []string
	for _, f := range mainModuleGoFiles(t) {
		if f.isTest() || !isGatewaySource(f.path) {
			continue
		}
		for i, line := range bytes.Split(f.src, []byte("\n")) {
			if strings.Contains(string(line), "time.Sleep(") {
				hits = append(hits, f.path+":"+strconv.Itoa(i+1))
			}
		}
	}
	if len(hits) > 0 {
		t.Errorf("time.Sleep inline di adapter/gateway (GAP-GW-03; pakai outq/clock injeksi): %v", hits)
	}
}

func isGatewaySource(path string) bool {
	return strings.HasPrefix(path, "internal/gateway/") ||
		strings.HasPrefix(path, "internal/gwplat/") ||
		strings.HasPrefix(path, "internal/gwwa/")
}
