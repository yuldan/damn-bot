//go:build !minimal

package gemini_test

import (
	"testing"

	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/credpool"
)

func TestCredentialPoolRotatesGeminiKey(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{status: 401, body: `{"error":{"code":401,"message":"bad key"}}`},
		reply{status: 400, body: `{"error":{"code":400,"message":"bad request"}}`})
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
	if len(hdrs) != 2 || hdrs[0].Get("x-goog-api-key") != "sk-a" || hdrs[1].Get("x-goog-api-key") != "sk-b" {
		t.Fatalf("x-goog-api-key sequence = %v", hdrs)
	}
}
