//go:build minimal

package main

import "testing"

// TestMinimalProcessSupportEmpty: the minimal build exposes no process_*
// tools and no shell background capability.
func TestMinimalProcessSupportEmpty(t *testing.T) {
	ps, err := newProcessSupport()
	if err != nil {
		t.Fatal(err)
	}
	if len(ps.tools) != 0 || ps.attach != nil || ps.close != nil {
		t.Fatalf("minimal process support = %+v", ps)
	}
}
