package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

func TestModuleAttachmentSpillsToBlob(t *testing.T) {
	t.Parallel()
	f := newDeferredFixture(t, []DeferredTool{deferredDef("code_read", "code", "read")}, ModeAsk, &approverRec{answer: ApprovalAllowOnce})
	data := []byte("\x89PNG\r\n\x1a\nfixture")
	f.mod.res = host.Result{Content: "ok", Attachments: []host.Attachment{{
		Kind: host.AttachmentImage, Name: "cat.png", MIME: "image/png", Data: data,
	}}}
	out := f.d.Dispatch(context.Background(), Call{
		TurnID: "t1", CallID: "c1", Name: ToolInvokeName,
		Args: json.RawMessage(`{"name":"code_read","arguments":{}}`),
	})
	if out.Err != nil {
		t.Fatalf("dispatch: %v", out.Err)
	}
	if len(out.Attachments) != 1 {
		t.Fatalf("attachments = %+v", out.Attachments)
	}
	ref := out.Attachments[0]
	if ref.Kind != "image" || ref.Name != "cat.png" || ref.MIME != "image/png" || ref.Size != int64(len(data)) {
		t.Fatalf("ref = %+v", ref)
	}
	if got := f.sp.m[ref.SHA]; !bytes.Equal(got, data) {
		t.Fatalf("spilled bytes = %q", got)
	}
	if out.Content != "ok" {
		t.Fatalf("content changed = %q", out.Content)
	}
}

func TestModuleTextResultHasNoAttachment(t *testing.T) {
	t.Parallel()
	f := newDeferredFixture(t, []DeferredTool{deferredDef("code_read", "code", "read")}, ModeAsk, &approverRec{answer: ApprovalAllowOnce})
	f.mod.res = host.Result{Content: "plain"}
	out := f.d.Dispatch(context.Background(), Call{
		TurnID: "t1", CallID: "c1", Name: ToolInvokeName,
		Args: json.RawMessage(`{"name":"code_read","arguments":{}}`),
	})
	if out.Err != nil || out.Content != "plain" || len(out.Attachments) != 0 {
		t.Fatalf("text control = %+v", out)
	}
}
