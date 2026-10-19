package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadInsideAutoAllModes(t *testing.T) {
	t.Parallel()
	for _, mode := range []Mode{ModeAsk, ModeAutoEdit, ModeAuto, ModeHeadless, ModeHeadlessAuto} {
		t.Run(modeName(mode), func(t *testing.T) {
			w := newWorkspace(t)
			w.write("a.txt", "alpha\n")
			d, j, _ := w.dispatcher(t, mode, nil)
			out := d.Dispatch(context.Background(), callOf("read_file", `{"path":"a.txt"}`))
			if out.Err != nil {
				t.Fatalf("read: %v", out.Err)
			}
			if out.Tainted || d.Tainted() {
				t.Fatal("trusted workspace read must not taint")
			}
			dc, rc := j.counts()
			if dc != 1 || rc != 1 {
				t.Fatalf("journal = %d/%d", dc, rc)
			}
		})
	}
}

func TestWriteModeMatrix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		mode     Mode
		approver *approverRec
		wantCode Code
		wantRun  bool
		wantAsk  int
	}{
		{ModeAsk, &approverRec{answer: ApprovalAllowOnce}, "", true, 1},
		{ModeAsk, &approverRec{answer: ApprovalDeny}, CodeDenied, false, 1},
		{ModeAsk, nil, CodeDenied, false, 0},
		{ModeAutoEdit, nil, "", true, 0},
		{ModeAuto, nil, "", true, 0},
		{ModeHeadlessAuto, nil, "", true, 0},
		{ModeHeadless, nil, CodeDenied, false, 0},
	}
	for _, c := range cases {
		t.Run(modeName(c.mode)+"/"+answerName(c.approver), func(t *testing.T) {
			w := newWorkspace(t)
			var appr Approver
			if c.approver != nil {
				appr = c.approver
			}
			d, _, _ := w.dispatcher(t, c.mode, appr)
			out := d.Dispatch(context.Background(), callOf("write_file", `{"path":"new.txt","content":"hello\n"}`))
			if codeOfOutcome(out) != c.wantCode {
				t.Fatalf("code = %s, want %s (%s)", codeOfOutcome(out), c.wantCode, out.Render())
			}
			_, err := os.Stat(filepath.Join(w.root, "new.txt"))
			if c.wantRun != (err == nil) {
				t.Fatalf("file exists = %v, want %v", err == nil, c.wantRun)
			}
			if c.approver != nil {
				if got := len(c.approver.requests()); got != c.wantAsk {
					t.Fatalf("approval prompts = %d, want %d", got, c.wantAsk)
				}
				for _, req := range c.approver.requests() {
					if !req.AllowSession || req.Class != "write" {
						t.Fatalf("write request = %+v", req)
					}
				}
			}
		})
	}
}

func TestOutsideReadAsks(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	appr := &approverRec{answer: ApprovalAllowOnce}
	d, _, _ := w.dispatcher(t, ModeAuto, appr)
	out := d.Dispatch(context.Background(), callOf("read_file", `{"path":`+quoteJSON(w.out)+`}`))
	if out.Err != nil {
		t.Fatalf("outside read: %v", out.Err)
	}
	if !strings.HasPrefix(out.Content, `<untrusted source="file:outside_workspace">`) {
		t.Fatalf("content = %q", out.Content)
	}
	if !out.Tainted || !d.Tainted() {
		t.Fatal("outside read must taint the turn")
	}
	reqs := appr.requests()
	if len(reqs) != 1 || !reqs[0].AllowSession || reqs[0].Class != "read" {
		t.Fatalf("requests = %+v", reqs)
	}
	if len(reqs[0].Paths) != 1 || !filepath.IsAbs(reqs[0].Paths[0]) {
		t.Fatalf("paths = %v", reqs[0].Paths)
	}
}

func TestOutsideHeadless(t *testing.T) {
	t.Parallel()
	for _, mode := range []Mode{ModeHeadless, ModeHeadlessAuto} {
		t.Run(modeName(mode), func(t *testing.T) {
			w := newWorkspace(t)
			d, _, _ := w.dispatcher(t, mode, nil)
			out := d.Dispatch(context.Background(), callOf("read_file", `{"path":`+quoteJSON(w.out)+`}`))
			if codeOfOutcome(out) != CodeOutsideWorkspace {
				t.Fatalf("code = %s", codeOfOutcome(out))
			}
			out = d.Dispatch(context.Background(), callOf("write_file", `{"path":`+quoteJSON(filepath.Join(filepath.Dir(w.root), "out.txt"))+`,"content":"x"}`))
			if codeOfOutcome(out) != CodeOutsideWorkspace {
				t.Fatalf("write code = %s", codeOfOutcome(out))
			}
		})
	}
}

func TestOutsideHeadlessAutoNoAsk(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	appr := &approverRec{answer: ApprovalAllowOnce}
	d, _, _ := w.dispatcher(t, ModeAuto, appr)
	out := d.Dispatch(context.Background(), callOf("write_file", `{"path":`+quoteJSON(filepath.Join(filepath.Dir(w.root), "out.txt"))+`,"content":"x"}`))
	if out.Err != nil {
		t.Fatalf("outside write: %v", out.Err)
	}
	if len(appr.requests()) != 1 {
		t.Fatalf("outside writes ask even in auto: %d", len(appr.requests()))
	}
}

func TestDeniedPathNeverApproved(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t, "**/.env")
	w.write(".env", "SECRET=1\n")
	for _, mode := range []Mode{ModeAsk, ModeAuto, ModeHeadless} {
		t.Run(modeName(mode), func(t *testing.T) {
			appr := &approverRec{answer: ApprovalAllowOnce}
			d, _, _ := w.dispatcher(t, mode, appr)
			for _, name := range []string{"read_file", "write_file"} {
				args := `{"path":".env"}`
				if name == "write_file" {
					args = `{"path":".env","content":"x"}`
				}
				out := d.Dispatch(context.Background(), callOf(name, args))
				if codeOfOutcome(out) != CodeDeniedPath {
					t.Fatalf("%s code = %s (%s)", name, codeOfOutcome(out), out.Render())
				}
			}
			if len(appr.requests()) != 0 {
				t.Fatal("deny_paths must never be approvable")
			}
			if got := w.read(".env"); got != "SECRET=1\n" {
				t.Fatalf("deny path was written: %q", got)
			}
		})
	}
}

func TestAllowSessionExactPath(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	appr := &approverRec{answer: ApprovalAllowSession}
	d, _, _ := w.dispatcher(t, ModeAsk, appr)
	first := d.Dispatch(context.Background(), callOf("write_file", `{"path":"a.txt","content":"one"}`))
	if first.Err != nil {
		t.Fatalf("first: %v", first.Err)
	}
	tag, _ := first.Meta["tag"].(string)
	if tag == "" {
		t.Fatalf("first write returned no tag: %v", first.Meta)
	}
	second := d.Dispatch(context.Background(), Call{Name: "write_file", Args: json.RawMessage(`{"path":"a.txt","tag":"` + tag + `","content":"two"}`)})
	if second.Err != nil {
		t.Fatalf("second: %v", second.Err)
	}
	if got := len(appr.requests()); got != 1 {
		t.Fatalf("allow_session repeated the prompt: %d", got)
	}
	third := d.Dispatch(context.Background(), Call{Name: "write_file", Args: json.RawMessage(`{"path":"b.txt","content":"three"}`)})
	if third.Err != nil {
		t.Fatalf("third: %v", third.Err)
	}
	if got := len(appr.requests()); got != 2 {
		t.Fatalf("a different path must ask again: %d", got)
	}
}

func TestTaintForcesApproval(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	w.write("a.txt", "a\n")
	appr := &approverRec{answer: ApprovalAllowOnce}
	d, _, _ := w.dispatcher(t, ModeAuto, appr)
	d.MarkTainted()

	// Reads stay automatic.
	if out := d.Dispatch(context.Background(), callOf("read_file", `{"path":"a.txt"}`)); out.Err != nil {
		t.Fatalf("read while tainted: %v", out.Err)
	}
	if len(appr.requests()) != 0 {
		t.Fatal("read asked while tainted")
	}

	out := d.Dispatch(context.Background(), callOf("write_file", `{"path":"b.txt","content":"x"}`))
	if out.Err != nil {
		t.Fatalf("tainted write: %v", out.Err)
	}
	reqs := appr.requests()
	if len(reqs) != 1 || !reqs[0].Tainted || reqs[0].AllowSession {
		t.Fatalf("tainted request = %+v", reqs)
	}

	// Headless + taint = denied.
	dh, _, _ := w.dispatcher(t, ModeHeadless, appr)
	dh.MarkTainted()
	if out := dh.Dispatch(context.Background(), callOf("write_file", `{"path":"c.txt","content":"x"}`)); codeOfOutcome(out) != CodeDenied {
		t.Fatalf("headless tainted code = %s", codeOfOutcome(out))
	}

	// A new turn clears taint.
	d.BeginTurn()
	if d.Tainted() {
		t.Fatal("taint survived BeginTurn")
	}
}

func TestApproverError(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	appr := &approverRec{err: errors.New("ui gone")}
	d, _, _ := w.dispatcher(t, ModeAsk, appr)
	out := d.Dispatch(context.Background(), callOf("write_file", `{"path":"a.txt","content":"x"}`))
	if codeOfOutcome(out) != CodeInternal {
		t.Fatalf("code = %s", codeOfOutcome(out))
	}
}

func TestTaintFromUntrustedReadResets(t *testing.T) {
	t.Parallel()
	w := newWorkspace(t)
	appr := &approverRec{answer: ApprovalAllowOnce}
	d, _, _ := w.dispatcher(t, ModeAuto, appr)
	_ = d.Dispatch(context.Background(), callOf("read_file", `{"path":`+quoteJSON(w.out)+`}`))
	if !d.Tainted() {
		t.Fatal("outside read did not taint")
	}
	d.BeginTurn()
	if d.Tainted() {
		t.Fatal("taint survived the next turn")
	}
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func modeName(m Mode) string {
	switch m {
	case ModeAsk:
		return "ask"
	case ModeAutoEdit:
		return "auto-edit"
	case ModeAuto:
		return "auto"
	case ModeHeadless:
		return "headless"
	default:
		return "headless-auto"
	}
}

func answerName(a *approverRec) string {
	if a == nil {
		return "none"
	}
	if a.answer == ApprovalDeny {
		return "deny"
	}
	return "allow"
}
