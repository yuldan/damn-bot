//go:build !minimal

package responses_test

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/credpool"
)

func TestCredentialPoolRotatesResponsesBearer(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{status: 401, body: `{"error":{"type":"invalid_request_error","message":"bad key"}}`},
		reply{status: 400, body: `{"error":{"type":"invalid_request_error","message":"bad request"}}`})
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
	hdrs := f.headers()
	if len(hdrs) != 2 || strings.TrimPrefix(hdrs[0].Get("Authorization"), "Bearer ") != "sk-a" ||
		strings.TrimPrefix(hdrs[1].Get("Authorization"), "Bearer ") != "sk-b" {
		t.Fatalf("Authorization sequence = %v", hdrs)
	}
}
