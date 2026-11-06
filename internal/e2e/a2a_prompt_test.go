package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat/a2a"
)

type a2aIDs struct{}

func (a2aIDs) Identity(_ context.Context, platform, externalID string) (gateway.Identity, bool, error) {
	return gateway.Identity{
		Platform: platform, ExternalID: externalID, Role: gateway.RoleOwner,
		Status: gateway.StatusAllowlisted, Strength: gateway.StrengthPlatformAuthenticated,
	}, true, nil
}

type a2aInbox struct {
	mu   sync.Mutex
	seen map[string]bool
}

func (i *a2aInbox) Record(_ context.Context, in *gateway.Inbound) (bool, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	key := in.Platform + "|" + in.UpdateID
	if i.seen[key] {
		return false, nil
	}
	i.seen[key] = true
	return true, nil
}

// TestF7A2AInboundPrompt is the cross-package acceptance of PLAT-001 and the
// A2A tools: an authenticated inbound task reaches exactly one gateway prompt
// turn, an unauthenticated request reaches none, and the outbound tool calls
// the same peer and returns the untrusted result.
func TestF7A2AInboundPrompt(t *testing.T) {
	gate, err := gateway.New(&gateway.Options{Identities: a2aIDs{}, Inbox: &a2aInbox{seen: map[string]bool{}}})
	if err != nil {
		t.Fatal(err)
	}
	var prompts atomic.Int64
	exec := a2a.ExecutorFunc(func(ctx context.Context, req a2a.Request) (string, error) {
		in := &gateway.Inbound{
			Platform: "a2a", Account: "agent", ChatID: "peer:1", SenderID: "peer:1",
			UpdateID: "a2a:" + req.TaskID, Text: req.Text(), Payload: `{"task":"` + req.TaskID + `"}`,
		}
		out, aerr := gate.Admit(ctx, in, func(context.Context, gateway.Ticket) error {
			prompts.Add(1)
			return nil
		})
		if aerr != nil {
			return "", fmt.Errorf("admit: %w", aerr)
		}
		if out.Kind != gateway.Accepted {
			return "", fmt.Errorf("admission rejected: %s", out.Reason)
		}
		return "pong", nil
	})
	bearer := strings.Join([]string{"e2e", "a2a", "token"}, "-")
	card := &a2a.AgentCard{
		Name: "damnbot", Description: "e2e agent", URL: "https://agent.example/a2a",
		Version: "1.0.0", ProtocolVersion: a2a.ProtocolVersion,
	}
	srv, err := a2a.New(t.Context(), &a2a.Config{Enabled: true, Card: card, Executor: exec, Token: bearer})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)

	// No bearer: 401 and zero prompts.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, hs.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"task/send","params":{"taskId":"x","message":{"role":"user","parts":[{"kind":"text","text":"halo"}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if resp.StatusCode != http.StatusUnauthorized || prompts.Load() != 0 {
		t.Fatalf("unauthenticated = %d prompts=%d", resp.StatusCode, prompts.Load())
	}

	client, err := a2a.NewClient(t.Context(), &a2a.ClientConfig{
		Peers: []a2a.Peer{{Name: "agent", URL: hs.URL}}, Token: bearer, AllowLoopback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := client.Send(t.Context(), "agent", a2a.Request{TaskID: "e2e-1", Message: a2a.Message{Role: "user", Parts: []a2a.Part{{Kind: "text", Text: "halo"}}}})
	if err != nil {
		t.Fatal(err)
	}
	final, err := client.Wait(t.Context(), "agent", task.ID, 5*time.Second)
	if err != nil || final.State != a2a.StateCompleted || !strings.Contains(final.Text, "<untrusted") {
		t.Fatalf("task = %+v, %v", final, err)
	}
	if prompts.Load() != 1 {
		t.Fatalf("prompts = %d, want 1", prompts.Load())
	}
	// A repeated task id must not produce a second prompt turn.
	if _, serr := client.Send(t.Context(), "agent", a2a.Request{TaskID: "e2e-1", Message: a2a.Message{Role: "user", Parts: []a2a.Part{{Kind: "text", Text: "halo"}}}}); serr != nil {
		t.Fatal(serr)
	}
	if prompts.Load() != 1 {
		t.Fatalf("replay prompts = %d, want 1", prompts.Load())
	}

	assertA2AToolOutbound(t, client, bearer, &prompts)
}

func assertA2AToolOutbound(t *testing.T, client *a2a.Client, bearer string, prompts *atomic.Int64) {
	t.Helper()
	// The outbound tool reaches the same prompt path and returns untrusted text.
	tool, ok := client.Tool(a2a.ToolCall)
	if !ok {
		t.Fatal("a2a_call missing")
	}
	args, err := json.Marshal(map[string]string{"agent": "agent", "task": "tool"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := tool.Invoke(t.Context(), args)
	if err != nil || !strings.HasPrefix(out, "<untrusted source=\"a2a\">") {
		t.Fatalf("tool = %q, %v", out, err)
	}
	if prompts.Load() != 2 {
		t.Fatalf("tool prompts = %d, want 2", prompts.Load())
	}
	if strings.Contains(out, bearer) {
		t.Fatal("tool output leaked the bearer token")
	}
}
