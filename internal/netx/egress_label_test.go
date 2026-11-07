package netx

import (
	"bytes"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// TestKanbanAttachRegistryEntry proves the attachment fetch is registered as
// tool egress: the registry carries the same purpose the production call site
// declares, and removing the entry makes the gate report it.
func TestKanbanAttachRegistryEntry(t *testing.T) {
	t.Parallel()
	e, ok := Registry()["kanban-attach"]
	if !ok {
		t.Fatal(`registry has no "kanban-attach" entry`)
	}
	if e.Purpose != Tool {
		t.Fatalf("kanban-attach purpose = %s, want tool", e.Purpose)
	}
	if e.Host == "" || e.Trigger == "" {
		t.Fatalf("kanban-attach entry is a placeholder: %+v", e)
	}
	if got := scanClientCalls(t)["kanban-attach"]; got != Tool {
		t.Fatalf("kanban-attach call site purpose = %s, want tool", got)
	}
	without := maps.Clone(Registry())
	delete(without, "kanban-attach")
	want := `egress "kanban-attach" has a production call site but no registry entry`
	if problems := registryProblems(without, scanClientCalls(t)); !slices.Contains(problems, want) {
		t.Fatalf("unregistered label passed the gate: %q", problems)
	}
}

// TestKanbanAttachLabelReachesTransport proves the attachment path carries the
// registry label into the transport: a request through the kanban-attach client
// logs exactly that egress point (the adapter's fake transport records it).
func TestKanbanAttachLabelReachesTransport(t *testing.T) {
	t.Parallel()
	var log bytes.Buffer
	network := New(Config{Log: slog.New(slog.NewTextHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	defer network.CloseIdleConnections()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("attachment")); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer srv.Close()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+"/note.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := network.Client("kanban-attach", Tool).Do(req)
	if err != nil {
		t.Fatalf("attach fetch: %v", err)
	}
	if _, cerr := io.Copy(io.Discard, resp.Body); cerr != nil {
		t.Fatalf("read: %v", cerr)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
	if !strings.Contains(log.String(), "egress=kanban-attach") {
		t.Fatalf("transport log has no egress=kanban-attach: %q", log.String())
	}
}
