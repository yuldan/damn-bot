package a2a

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gwplat"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func clientFor(t *testing.T, url, token string) *Client {
	t.Helper()
	c, err := NewClient(t.Context(), &ClientConfig{
		Peers:         []Peer{{Name: "local", URL: url}},
		Token:         token,
		AllowLoopback: true,
		Timeout:       2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestF7A2AClient covers the outbound surface: send/get/status/cancel/wait,
// SSE streaming, discovery, the pre-dial SSRF gate, and a token-free error.
func TestF7A2AClient(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	exec := ExecutorFunc(func(ctx context.Context, req Request) (string, error) {
		if req.Text() == "block" {
			select {
			case <-release:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return "echo:" + req.Text(), nil
	})
	srv, url := newTestServer(t, exec)
	client := clientFor(t, url, testToken)
	assertClientCalls(t, client)
	assertClientCancel(t, srv, client, release)
	assertClientStreamDiscover(t, client)
	assertClientSSRF(t)
	assertClientBadToken(t, url)
}

func assertClientCalls(t *testing.T, client *Client) {
	t.Helper()
	task, err := client.Send(t.Context(), "local", Request{Message: Message{Role: "user", Parts: []Part{{Text: "hi"}}}})
	if err != nil {
		t.Fatal(err)
	}
	final, err := client.Wait(t.Context(), "local", task.ID, 2*time.Second)
	if err != nil || final.State != StateCompleted || !strings.Contains(final.Text, "echo:hi") {
		t.Fatalf("wait = %+v, %v", final, err)
	}
	status, err := client.Status(t.Context(), "local", task.ID)
	if err != nil || status.State != StateCompleted {
		t.Fatalf("status = %+v, %v", status, err)
	}
	if hist := client.History(); len(hist) == 0 {
		t.Fatal("history is empty")
	}
	if peers := client.Peers(); len(peers) != 1 || peers[0].Name != "local" {
		t.Fatalf("peers = %+v", peers)
	}
	_, err = client.Get(t.Context(), "local", "missing")
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("missing task = %v", err)
	}
}

func assertClientCancel(t *testing.T, srv *Server, client *Client, release chan struct{}) {
	t.Helper()
	blocked, err := client.Send(t.Context(), "local", Request{TaskID: "blocked", Message: Message{Role: "user", Parts: []Part{{Text: "block"}}}})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, srv, blocked.ID, StateWorking)
	canceled, err := client.Cancel(t.Context(), "local", blocked.ID)
	if err != nil || canceled.State != StateCanceled {
		t.Fatalf("cancel = %+v, %v", canceled, err)
	}
	close(release)
}

func assertClientStreamDiscover(t *testing.T, client *Client) {
	t.Helper()
	var events []Task
	err := client.Stream(t.Context(), "local", Request{Message: Message{Role: "user", Parts: []Part{{Text: "stream"}}}}, func(task Task) error {
		events = append(events, task)
		return nil
	})
	if err != nil || len(events) == 0 || !events[len(events)-1].State.Terminal() {
		t.Fatalf("stream = %v events=%+v", err, events)
	}
	card, err := client.Discover(t.Context(), "local")
	if err != nil || card.Name != "damnbot" {
		t.Fatalf("discover = %+v, %v", card, err)
	}
	missing, err := NewClient(t.Context(), &ClientConfig{
		Peers: []Peer{{Name: "missing", URL: missingCardURL(t)}}, Token: testToken, AllowLoopback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = missing.Discover(t.Context(), "missing")
	if !errors.Is(err, gwplat.ErrUnavailable) || !errors.Is(err, ErrCardAbsent) {
		t.Fatalf("absent peer card = %v, want module_unavailable{a2a_card_absent}", err)
	}
}

func assertClientSSRF(t *testing.T) {
	t.Helper()
	var dials atomic.Int64
	ssrfHTTP := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		dials.Add(1)
		return nil, errors.New("must not dial")
	})}
	_, err := NewClient(t.Context(), &ClientConfig{
		Peers: []Peer{{Name: "private", URL: "http://127.0.0.1:9999/"}}, Token: testToken, HTTP: ssrfHTTP,
	})
	if !errors.Is(err, ErrPeerURL) || dials.Load() != 0 {
		t.Fatalf("SSRF peer = %v dials=%d", err, dials.Load())
	}
	for _, raw := range []string{"http://10.0.0.8/", "http://localhost:1/", "https://169.254.169.254/", "file:///etc/passwd"} {
		if verr := ValidatePeerURL(raw, false); !errors.Is(verr, ErrPeerURL) {
			t.Errorf("%s = %v, want ErrPeerURL", raw, verr)
		}
	}
}

func assertClientBadToken(t *testing.T, url string) {
	t.Helper()
	bad, err := NewClient(t.Context(), &ClientConfig{
		Peers: []Peer{{Name: "local", URL: url}}, Token: "wrong-token-value", AllowLoopback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = bad.Send(t.Context(), "local", Request{Message: Message{Role: "user", Parts: []Part{{Text: "x"}}}})
	if !errors.Is(err, ErrAuth) || strings.Contains(err.Error(), "wrong-token-value") {
		t.Fatalf("bad token = %v", err)
	}
}

func missingCardURL(t *testing.T) string {
	t.Helper()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	t.Cleanup(hs.Close)
	return hs.URL
}
