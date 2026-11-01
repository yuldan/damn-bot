//go:build !minimal

package anthropic_test

import (
	"testing"

	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/credpool"
)

func TestCredentialPoolRotatesMessagesKey(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{status: 401, body: `{"type":"error","error":{"type":"authentication_error"}}`},
		reply{status: 400, body: `{"type":"error","error":{"type":"invalid_request_error"}}`})
	pool, err := credpool.New([]credpool.Credential{
		credpool.NewCredential("a", "sk-a"), credpool.NewCredential("b", "sk-b"),
	})
	if err != nil {
		t.Fatal(err)
	}
	rt := route("m", f.URL())
	provider.WithCredentialPool(&rt, pool)
	o := newClient(t, []provider.Route{rt}, nil, 0)
	if _, err := o.Do(t.Context(), provider.PurposeTurn, ask("hi"), nil); err == nil {
		t.Fatal("expected the scripted 400")
	}
	_, _, hdrs := f.requests()
	if len(hdrs) != 2 || hdrs[0].Get("x-api-key") != "sk-a" || hdrs[1].Get("x-api-key") != "sk-b" {
		t.Fatalf("x-api-key sequence = %v", hdrs)
	}
}
