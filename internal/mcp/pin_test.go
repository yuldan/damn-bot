package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
)

func echoPages(description string) [][]Tool {
	return [][]Tool{{{Name: "echo", Description: description, InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)}}}
}

func mustPin(t *testing.T, pins PinStore, tool string) Pin {
	t.Helper()
	pin, found, err := pins.Pin(context.Background(), "fake", tool)
	if err != nil || !found {
		t.Fatalf("pin fake/%s = %+v, found=%v, err=%v", tool, pin, found, err)
	}
	return pin
}

func TestPinFirstListingIsPinned(t *testing.T) { //nolint:gocyclo // one scenario walks pin, refresh, listing and call
	t.Parallel()
	srv := newFakeServer()
	pins := newPins()
	client, _ := newPinnedStdioClient(t, srv, Server{ReadOnly: map[string]bool{"echo": true}}, host.Capabilities{}, pins)
	tools, err := client.ListTools(context.Background())
	if err != nil || len(tools) != 1 {
		t.Fatalf("ListTools = %+v, %v", tools, err)
	}
	all, perr := client.Pins(context.Background())
	if perr != nil || len(all) != 1 || all[0].Tool != "echo" {
		t.Fatalf("Pins = %+v, %v", all, perr)
	}
	pin := mustPin(t, pins, "echo")
	if pin.State != PinPinned || pin.ApprovedAt == 0 || pin.ApprovedBy != "first-seen" {
		t.Fatalf("first pin = %+v", pin)
	}
	if len(pin.SchemaSHA256) != 64 || strings.Contains(pin.Diff, "properties") {
		t.Fatalf("pin stores raw schema or a bad digest: %+v", pin)
	}
	// The same definition never rewrites the pin or trips quarantine.
	if _, rerr := client.RefreshTools(context.Background()); rerr != nil {
		t.Fatalf("RefreshTools: %v", rerr)
	}
	again := mustPin(t, pins, "echo")
	if again.State != PinPinned || again.ApprovedAt != pin.ApprovedAt {
		t.Fatalf("re-pin = %+v", again)
	}
	res, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"hi"}`), "c1")
	if err != nil || res.Content != "ok" {
		t.Fatalf("pinned call = %+v, %v", res, err)
	}
}

func TestPinChangedSchemaQuarantinesAndReview(t *testing.T) { //nolint:gocyclo // deny and allow review in one scenario
	t.Parallel()
	srv := newFakeServer()
	pins := newPins()
	approver := &fakeApprover{answer: host.ApprovalDeny}
	client, _ := newPinnedStdioClient(t, srv, Server{ReadOnly: map[string]bool{"echo": true}},
		host.Capabilities{Approvals: approver}, pins)
	if _, err := client.ListTools(context.Background()); err != nil {
		t.Fatalf("first list: %v", err)
	}
	srv.mu.Lock()
	srv.pages = echoPages("echo with a new rug-pull description")
	srv.mu.Unlock()
	tools, err := client.RefreshTools(context.Background())
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if len(tools) != 0 {
		t.Fatalf("quarantined tool leaked into the catalog: %+v", tools)
	}
	pin := mustPin(t, pins, "echo")
	if pin.State != PinChanged {
		t.Fatalf("pin after change = %+v", pin)
	}
	if !strings.Contains(pin.Diff, `"change":"modified"`) || strings.Contains(pin.Diff, "rug-pull description") {
		t.Fatalf("diff summary = %q", pin.Diff)
	}
	_, err = client.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"write"}`), "c1")
	if code := codeOf(t, err); code != "module_unavailable" || !strings.Contains(err.Error(), "mcp_tool_changed") {
		t.Fatalf("quarantined call = %v", err)
	}
	if _, _, calls := srv.counts(); calls != 0 {
		t.Fatalf("quarantined call reached the server %d times", calls)
	}
	// Review is a consent prompt; the default answer is deny and nothing moves.
	if rerr := client.Review(context.Background(), "echo", "operator"); !errors.Is(rerr, ErrDenied) {
		t.Fatalf("review deny = %v", rerr)
	}
	if got := mustPin(t, pins, "echo"); got.State != PinChanged {
		t.Fatalf("denied review changed the pin: %+v", got)
	}
	approver.mu.Lock()
	approver.answer = host.ApprovalAllowOnce
	approver.mu.Unlock()
	if rerr := client.Review(context.Background(), "echo", "operator"); rerr != nil {
		t.Fatalf("review allow = %v", rerr)
	}
	if got := mustPin(t, pins, "echo"); got.State != PinPinned || got.ApprovedBy != "operator" {
		t.Fatalf("approved pin = %+v", got)
	}
	res, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"write"}`), "c2")
	if err != nil || res.Content != "ok" {
		t.Fatalf("call after review = %+v, %v", res, err)
	}
}

func TestPinNewAndRemovedToolsQuarantine(t *testing.T) {
	t.Parallel()
	srv := newFakeServer()
	srv.pages = echoPages("echo")
	pins := newPins()
	client, _ := newPinnedStdioClient(t, srv, Server{ReadOnly: map[string]bool{"added": true}}, host.Capabilities{}, pins)
	if _, err := client.ListTools(context.Background()); err != nil {
		t.Fatalf("first list: %v", err)
	}
	addedTool := Tool{Name: "added", Description: "new after review", InputSchema: json.RawMessage(`{"type":"object"}`)}
	srv.mu.Lock()
	srv.pages = [][]Tool{{echoPages("echo")[0][0], addedTool}}
	srv.mu.Unlock()
	tools, err := client.RefreshTools(context.Background())
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("tools after add = %+v, %v", tools, err)
	}
	added := mustPin(t, pins, "added")
	if added.State != PinChanged || !strings.Contains(added.Diff, `"change":"added"`) {
		t.Fatalf("new tool pin = %+v", added)
	}
	if _, err := client.CallTool(context.Background(), "added", json.RawMessage(`{}`), "c1"); codeOf(t, err) != "module_unavailable" {
		t.Fatalf("new tool call = %v", err)
	}
	srv.mu.Lock()
	srv.pages = echoPages("echo")
	srv.mu.Unlock()
	if _, err := client.RefreshTools(context.Background()); err != nil {
		t.Fatalf("refresh removal: %v", err)
	}
	removed := mustPin(t, pins, "added")
	if removed.State != PinQuarantined || !strings.Contains(removed.Diff, `"change":"removed"`) {
		t.Fatalf("removed tool pin = %+v", removed)
	}
}

func TestPinListChangedNotificationRefetches(t *testing.T) {
	t.Parallel()
	srv := newFakeServer()
	pins := newPins()
	client, _ := newPinnedStdioClient(t, srv, Server{}, host.Capabilities{}, pins)
	if _, err := client.ListTools(context.Background()); err != nil {
		t.Fatalf("first list: %v", err)
	}
	_, lists, _ := srv.counts()
	// Negative control: an unrelated notification does not re-fetch.
	srv.sendNotification(t, "notifications/progress")
	time.Sleep(20 * time.Millisecond) //nolint:forbidigo // asserting the absence of an async re-fetch
	if _, after, _ := srv.counts(); after != lists {
		t.Fatalf("unrelated notification re-fetched: %d -> %d", lists, after)
	}
	srv.mu.Lock()
	srv.pages = echoPages("changed via notification")
	srv.mu.Unlock()
	srv.sendNotification(t, notifyListChanged)
	eventually(t, func() bool {
		return mustPin(t, pins, "echo").State == PinChanged
	})
	if _, after, _ := srv.counts(); after != lists+1 {
		t.Fatalf("list_changed lists = %d, want %d", after, lists+1)
	}
	if _, err := client.CallTool(context.Background(), "echo", json.RawMessage(`{}`), "c1"); codeOf(t, err) != "module_unavailable" {
		t.Fatalf("call after notification = %v", err)
	}
}

func TestPinStoreIsRequired(t *testing.T) {
	t.Parallel()
	srv := newFakeServer()
	h := newSpawnHost(t, newStore(), srv)
	h.caps.Secrets = &fakeSecrets{}
	if _, err := NewClient(h, Server{Name: "fake", Command: []string{"x"}}, nil, nil); !errors.Is(err, ErrPinsUnavailable) {
		t.Fatalf("NewClient without pins = %v", err)
	}
}

func TestPinDigestStableAcrossAnnotationOrder(t *testing.T) {
	t.Parallel()
	first := Tool{
		Name: "t", Description: "d",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Annotations: json.RawMessage(`{"a":1,"b":2}`),
	}
	second := first
	second.Annotations = json.RawMessage(`{ "b": 2, "a": 1 }`)
	d1, err1 := toolDigest(&first)
	d2, err2 := toolDigest(&second)
	if err1 != nil || err2 != nil || d1 != d2 {
		t.Fatalf("digest = %s/%v vs %s/%v", d1, err1, d2, err2)
	}
	second.Description = "other"
	d3, derr := toolDigest(&second)
	if derr != nil {
		t.Fatalf("digest: %v", derr)
	}
	if d3 == d1 {
		t.Fatal("description change did not change the digest")
	}
}

func TestPinApproveIsConditionalOnDigest(t *testing.T) {
	t.Parallel()
	pins := newPins()
	changed := Pin{Server: "s", Tool: "t", SchemaSHA256: strings.Repeat("a", 64), FirstSeen: 1, State: PinChanged, Diff: `{}`}
	if err := pins.PutPin(context.Background(), changed); err != nil {
		t.Fatal(err)
	}
	if ok, err := pins.ApprovePin(context.Background(), "s", "t", strings.Repeat("b", 64), "op", 2); err != nil || ok {
		t.Fatalf("stale digest approved: ok=%v err=%v", ok, err)
	}
	if ok, err := pins.ApprovePin(context.Background(), "s", "t", changed.SchemaSHA256, "op", 2); err != nil || !ok {
		t.Fatalf("matching digest refused: ok=%v err=%v", ok, err)
	}
	if ok, err := pins.ApprovePin(context.Background(), "s", "t", changed.SchemaSHA256, "op", 3); err != nil || ok {
		t.Fatalf("pinned review approved twice: ok=%v err=%v", ok, err)
	}
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond) //nolint:forbidigo // polling the notification's async refresh
	}
	t.Fatal("condition was not met within 2s")
}
