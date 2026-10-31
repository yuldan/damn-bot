package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/netx/retry"
	"github.com/damnbot/damnbot/internal/provider/auth"
)

// TestRouteAuthSigner: a Route with Auth replaces the static bearer and the
// signer runs on the exact request that reaches the wire.
func TestRouteAuthSigner(t *testing.T) {
	t.Parallel()
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\n\n" + //nolint:errcheck // test SSE
			"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()
	c := netx.New(netx.Config{}).Client("test", netx.Inference)
	rt := &Route{
		Provider: "custom", Model: "m", BaseURL: srv.URL, APIKey: "static-key",
		Auth: auth.Bearer("signed-token"),
	}
	if _, err := attempt(context.Background(), c, rt, []byte(`{}`), 1, nil); err != nil {
		t.Fatal(err)
	}
	if got.Get("Authorization") != "Bearer signed-token" {
		t.Fatalf("signer header missing: %v", got)
	}
}

// TestRouteAuthFailureIsAuthClass: a signer that cannot produce a credential
// fails the attempt as auth (non-retryable, no request sent).
func TestRouteAuthFailureIsAuthClass(t *testing.T) {
	t.Parallel()
	c := netx.New(netx.Config{}).Client("test", netx.Inference)
	rt := &Route{Provider: "custom", Model: "m", BaseURL: "http://127.0.0.1:1", Auth: auth.Bearer("")}
	_, err := attempt(context.Background(), c, rt, []byte(`{}`), 1, nil)
	var perr *Error
	if !errors.As(err, &perr) || perr.Class != retry.Auth {
		t.Fatalf("err = %v, want auth class", err)
	}
}
