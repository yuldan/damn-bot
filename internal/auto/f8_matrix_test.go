//go:build !minimal

package auto

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/provider"
)

// TestF8AUTOMatrix adalah agregat §1 "AUTO 32/32": dua belas fitur F8 berada
// di registry tertutup (AUTO-001..032), semuanya default off, dan pemanggilan
// dengan konfigurasi default tidak menghasilkan satu pun call provider.
func TestF8AUTOMatrix(t *testing.T) {
	t.Parallel()
	defs := checkAUTORegistry(t)
	checkAUTODocs(t)
	checkAUTODefaultOff(t, defs)
}

// checkAUTORegistry verifies the closed twelve-row registry and its parity IDs.
func checkAUTORegistry(t *testing.T) []Definition {
	t.Helper()
	want := map[Feature]string{
		Review: "AUTO-001", Title: "AUTO-002", Curator: "AUTO-003",
		Compaction: "AUTO-004", Blueprints: "AUTO-012", Suggestions: "AUTO-013",
		Goals: "AUTO-014", Loops: "AUTO-015", Heartbeat: "AUTO-016",
		DiskCleanup: "AUTO-030", Maintenance: "AUTO-031", OutboundWebhook: "AUTO-032",
	}
	defs := Definitions()
	if len(defs) != len(want) || len(defs) != 12 {
		t.Fatalf("registry = %d features, want 12", len(defs))
	}
	for _, d := range defs {
		id, ok := want[d.Feature]
		if !ok {
			t.Errorf("unexpected registry feature %q", d.Feature)
			continue
		}
		if d.ID != id {
			t.Errorf("%s: parity row %q, want %q", d.Feature, d.ID, id)
		}
		delete(want, d.Feature)
	}
	for f := range want {
		t.Errorf("registry is missing %q", f)
	}
	return defs
}

// checkAUTODocs pins 32 AUTO parity rows (12 F8) and the closed ledger rows.
func checkAUTODocs(t *testing.T) {
	t.Helper()
	parity := autoDocRows(t, "../../docs/FEATURE-PARITY.md")
	if len(parity) != 32 {
		t.Fatalf("parity AUTO rows = %d, want 32", len(parity))
	}
	f8 := 0
	for _, cells := range parity {
		if len(cells) >= 9 && cells[7] == "F8" {
			f8++
		}
	}
	if f8 != 12 {
		t.Fatalf("parity AUTO rows with fase F8 = %d, want 12", f8)
	}
	ledger := autoDocRows(t, "../../docs/F8-ACCEPTANCE.md")
	if len(ledger) != 12 {
		t.Fatalf("ledger AUTO rows = %d, want 12", len(ledger))
	}
	for _, cells := range ledger {
		if len(cells) < 6 || cells[4] != "LULUS" {
			t.Errorf("%s: status %q, want LULUS", cellsAt(cells, 0), cellsAt(cells, 4))
		}
	}
}

// checkAUTODefaultOff proves the zero config enables nothing and calls nothing.
func checkAUTODefaultOff(t *testing.T, defs []Definition) {
	t.Helper()
	fake := &fakeClient{resp: provider.Response{Content: "x"}}
	s := mustNew(t, Config{Model: "fake/model"}, fake)
	var events []Event
	s.SetEventSink(func(ev Event) { events = append(events, ev) })
	for _, d := range defs {
		if s.Enabled(d.Feature) {
			t.Errorf("%s is enabled by default", d.Feature)
		}
		if d.DefaultDailyTokens <= 0 {
			t.Errorf("%s has no positive default budget", d.Feature)
		}
		if _, err := s.Call(context.Background(), d.Feature, "hi"); !errors.Is(err, ErrDisabled) {
			t.Errorf("%s: err = %v, want ErrDisabled", d.Feature, err)
		}
	}
	if got := fake.count(); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
	if len(events) != len(defs) {
		t.Fatalf("events = %d, want %d", len(events), len(defs))
	}
	for i, ev := range events {
		if ev.Called || ev.Feature != defs[i].Feature || ev.Denied != ErrDisabled.Error() {
			t.Errorf("event %d = %+v, want a disabled event for %s", i, ev, defs[i].Feature)
		}
		if ev.Purpose == provider.PurposeAuto {
			t.Errorf("%s emitted purpose=auto while disabled", ev.Feature)
		}
	}
}

// TestF8AutoDefaultZeroCalls adalah bukti P11 untuk sisi call: registry
// default tanpa section aktif tidak mengirim satu pun request purpose=auto.
func TestF8AutoDefaultZeroCalls(t *testing.T) {
	t.Parallel()
	fake := &fakeClient{resp: provider.Response{Content: "never"}}
	s := mustNew(t, Config{Model: "fake/model"}, fake)
	var purposes []provider.Purpose
	s.SetEventSink(func(ev Event) {
		if ev.Called || ev.Purpose == provider.PurposeAuto {
			purposes = append(purposes, ev.Purpose)
		}
	})
	for _, d := range Definitions() {
		if _, err := s.Call(context.Background(), d.Feature, "prompt"); !errors.Is(err, ErrDisabled) {
			t.Fatalf("%s: err = %v, want ErrDisabled", d.Feature, err)
		}
	}
	if got := fake.count(); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
	if len(purposes) != 0 {
		t.Fatalf("purpose=auto events = %v, want none", purposes)
	}
}

// TestF8AutoBudgetStop membuktikan P11 untuk setiap fitur: request dipotong ke
// sisa budget harian, fitur berhenti tepat di budget, dan setiap event yang
// terkirim membawa feature=<id> serta purpose=auto.
func TestF8AutoBudgetStop(t *testing.T) {
	t.Parallel()
	for _, d := range Definitions() {
		t.Run(string(d.Feature), func(t *testing.T) {
			t.Parallel()
			checkFeatureBudgetStop(t, d)
		})
	}
}

// checkFeatureBudgetStop runs one feature at a 6-token daily budget: two calls
// clamp to 6/3 tokens, the third is denied, and every event is purpose=auto.
func checkFeatureBudgetStop(t *testing.T, d Definition) {
	t.Helper()
	const daily = 6
	client := &budgetClient{}
	s := mustNew(t, Config{
		Model:    "fake/model",
		Features: map[Feature]Limits{d.Feature: {Enabled: true, DailyTokens: daily}},
	}, client)
	var events []Event
	s.SetEventSink(func(ev Event) { events = append(events, ev) })

	for i := range 2 {
		if _, err := s.Call(context.Background(), d.Feature, "x"); err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	if _, err := s.Call(context.Background(), d.Feature, "x"); !errors.Is(err, ErrBudget) {
		t.Fatalf("call 3: err = %v, want ErrBudget", err)
	}
	if got := client.calls(); got != 2 {
		t.Fatalf("provider calls = %d, want 2", got)
	}
	if got, want := client.maxTokens(), []int{daily, daily / 2}; !slices.Equal(got, want) {
		t.Fatalf("max_tokens = %v, want %v (remaining daily budget)", got, want)
	}
	if tokens, _ := s.Spent(d.Feature); tokens != daily {
		t.Fatalf("spent = %d tokens, want %d", tokens, daily)
	}
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
	for i, ev := range events[:2] {
		if !ev.Called || ev.Feature != d.Feature || ev.Purpose != provider.PurposeAuto {
			t.Errorf("event %d = %+v, want called purpose=auto feature=%s", i, ev, d.Feature)
		}
	}
	if last := events[2]; last.Called || last.Feature != d.Feature || last.Denied != ErrBudget.Error() {
		t.Errorf("last event = %+v, want ErrBudget for %s", last, d.Feature)
	}
}

// budgetClient answers every request with one input token plus at most two
// output tokens, and records the request ceiling so the test can prove the
// remaining-budget clamp rather than a fixed provider response.
type budgetClient struct {
	mu   sync.Mutex
	n    int
	reqs []provider.Request
}

func (c *budgetClient) Do(_ context.Context, _ provider.Purpose, r *provider.Request, _ func(provider.Chunk)) (provider.Response, error) {
	c.mu.Lock()
	c.n++
	c.reqs = append(c.reqs, *r)
	c.mu.Unlock()
	out := int64(min(r.MaxTokens, 2))
	return provider.Response{Content: "ok", Usage: budget.Usage{Input: 1, Output: out}}, nil
}

func (c *budgetClient) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func (c *budgetClient) maxTokens() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]int, 0, len(c.reqs))
	for _, r := range c.reqs {
		out = append(out, r.MaxTokens)
	}
	return out
}

// autoDocRows returns the cells of every table row whose first cell starts with
// "AUTO-"; the ledger gate in internal/archtest owns full status parsing.
func autoDocRows(t *testing.T, path string) [][]string {
	t.Helper()
	raw, err := os.ReadFile(path) // #nosec G304 -- repository file
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var rows [][]string
	for line := range strings.Lines(string(raw)) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "| AUTO-") {
			continue
		}
		cells := strings.Split(line, "|")
		if len(cells) < 3 {
			continue
		}
		row := make([]string, 0, len(cells)-2)
		for _, cell := range cells[1 : len(cells)-1] {
			row = append(row, strings.TrimSpace(cell))
		}
		rows = append(rows, row)
	}
	return rows
}

func cellsAt(cells []string, i int) string {
	if i < 0 || i >= len(cells) {
		return ""
	}
	return cells[i]
}
