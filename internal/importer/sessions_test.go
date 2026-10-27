package importer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func sessionImporter(t *testing.T, src *memSessionSource) (*Importer, *memSink) {
	t.Helper()
	sink := newMemSink()
	imp, err := New(&Deps{
		ReadFile: os.ReadFile, WalkDir: filepath.WalkDir,
		Sink: sink, SessionSink: sink, Secrets: &memSecrets{values: map[string]string{}},
		Sessions: src,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return imp, sink
}

func TestImportSessions(t *testing.T) {
	t.Parallel()
	src := &memSessionSource{sessions: []Session{
		{SourceID: "s1", Title: "first", Model: "openai/gpt", Messages: []SessionMessage{
			{SourceID: 1, Timestamp: 1000, Role: "user", Content: "hello"},
			{SourceID: 2, Timestamp: 2000, Role: "assistant", Content: "hi"},
		}},
		{SourceID: "s2", Messages: []SessionMessage{
			{SourceID: 3, Role: "tool", Content: "out", ToolName: "shell"},
		}, Skipped: 5},
	}}
	imp, sink := sessionImporter(t, src)
	report, err := imp.Import(context.Background(), t.TempDir(), Options{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if report.Sessions.Total != 3 || report.Sessions.Imported != 2 {
		t.Fatalf("sessions report = %+v", report.Sessions)
	}
	if len(sink.sessions) != 2 {
		t.Fatalf("stored %d sessions", len(sink.sessions))
	}
	first := sink.sessions["s1"]
	if first.Title != "first" || len(first.Messages) != 2 || first.Messages[0].Content != "hello" {
		t.Errorf("s1 = %+v", first)
	}
	if first.Messages[1].Timestamp != 2000 {
		t.Errorf("order/timestamp lost: %+v", first.Messages)
	}
	if skip := report.Sessions.Skipped; len(skip) != 1 || !strings.Contains(skip[0].Reason, "5 messages beyond") {
		t.Errorf("skips = %+v", skip)
	}
	if report.Coverage() != 1 {
		t.Errorf("coverage = %v", report.Coverage())
	}
}

func TestImportSessionsReportsBadRoleAndClamps(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("x", MaxSessionMessageBytes+10)
	src := &memSessionSource{sessions: []Session{{
		SourceID: "s1",
		Messages: []SessionMessage{
			{SourceID: 1, Role: "weird", Content: "unknown"},
			{SourceID: 2, Role: "user", Content: big},
		},
	}}}
	imp, sink := sessionImporter(t, src)
	report, err := imp.Import(context.Background(), t.TempDir(), Options{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(report.Sessions.Skipped) != 1 || !strings.Contains(report.Sessions.Skipped[0].Reason, "unknown role") {
		t.Fatalf("skips = %+v", report.Sessions.Skipped)
	}
	if len(report.Clamps) != 1 || report.Clamps[0].To > MaxSessionMessageBytes+3 {
		t.Fatalf("clamps = %+v", report.Clamps)
	}
	got := sink.sessions["s1"].Messages
	if len(got) != 1 || !utf8.ValidString(got[0].Content) || len(got[0].Content) > MaxSessionMessageBytes+3 {
		t.Errorf("clamped message = %d bytes, valid=%t", len(got[0].Content), utf8.ValidString(got[0].Content))
	}
}

func TestImportSessionsUnsupportedDoesNotRead(t *testing.T) {
	t.Parallel()
	src := &memSessionSource{sessions: []Session{{SourceID: "s1"}}}
	imp, sink := sessionImporter(t, src)
	report, err := imp.Import(context.Background(), t.TempDir(), Options{
		Unsupported: map[Source]string{SourceSessions: "not selected (--only)"},
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if src.calls != 0 {
		t.Errorf("unsupported source read %d times", src.calls)
	}
	if report.Sessions.Total != 1 || len(report.Sessions.Skipped) != 1 ||
		report.Sessions.Skipped[0].Reason != "not selected (--only)" {
		t.Errorf("sessions report = %+v", report.Sessions)
	}
	if sink.calls != 0 {
		t.Errorf("unsupported source wrote %d items", sink.calls)
	}
}

func TestImportSessionsDryRun(t *testing.T) {
	t.Parallel()
	src := &memSessionSource{sessions: []Session{{SourceID: "s1", Messages: []SessionMessage{
		{SourceID: 1, Role: "user", Content: "hello"},
	}}}}
	imp, sink := sessionImporter(t, src)
	report, err := imp.Import(context.Background(), t.TempDir(), Options{DryRun: true})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if sink.calls != 0 {
		t.Errorf("dry run wrote %d items", sink.calls)
	}
	if report.Sessions.Imported != 1 || report.Sessions.Changed != 0 {
		t.Errorf("sessions report = %+v", report.Sessions)
	}
}
