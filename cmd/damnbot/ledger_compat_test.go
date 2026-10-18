package main

import (
	"testing"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/ctxdb/ledgerstore"
)

// TestLedgerTypesConvertible (ADR-101 butir 5): the composition root turns
// budget ledger values into ledgerstore rows (and back) by plain type
// conversion; a field added to one side only fails to compile here.
func TestLedgerTypesConvertible(t *testing.T) {
	t.Parallel()
	cost := int64(7)
	e := budget.Entry{SessionID: "s", InputTokens: 3, CostMicros: &cost, TTFTMS: -1}
	if r := ledgerstore.Row(e); budget.Entry(r) != e {
		t.Errorf("round trip %+v", r)
	}
	if s := budget.Sample(ledgerstore.Sample{Real: 2, Est: 3}); s.Est != 3 {
		t.Errorf("sample %+v", s)
	}
}
