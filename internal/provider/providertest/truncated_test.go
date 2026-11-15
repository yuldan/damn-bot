package providertest

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestServerIgnoresTruncatedRequestBody pins the killed-client path of
// TestRunKilledMidTurn: a process SIGKILLed while it uploads a request leaves
// the server with an unexpected EOF. That must not be reported as a script
// violation (tb.Errorf), because it is exactly what the crash test produces;
// the reply script must stay unconsumed.
func TestServerIgnoresTruncatedRequestBody(t *testing.T) {
	t.Parallel()
	s := New(t, nil, Reply{})
	pr, pw := io.Pipe()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, s.URL+"/chat/completions", pr)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, werr := pw.Write([]byte(`{"model":"x"`)); werr != nil {
			return
		}
		_ = pw.CloseWithError(errors.New("client killed"))
	}()
	res, err := s.srv.Client().Do(req)
	if err == nil {
		if cerr := res.Body.Close(); cerr != nil {
			t.Errorf("close truncated response body: %v", cerr)
		}
	}
	<-done
	// Close blocks until the handler has returned, so an Errorf (which would
	// fail this test) cannot race the assertion.
	s.srv.Close()
	if got := s.Requests(); len(got) != 0 {
		t.Fatalf("aborted request was recorded: %d", len(got))
	}
	s.mu.Lock()
	left := len(s.script)
	s.mu.Unlock()
	if left != 1 {
		t.Fatalf("script consumed by an aborted request: %d replies left", left)
	}
	if !strings.Contains(s.URL, "/v1") {
		t.Fatalf("URL = %q", s.URL)
	}
}
