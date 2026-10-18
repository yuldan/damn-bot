// Package providertest is the deterministic fake provider of E-69: an
// OpenAI-compatible chat/completions server on a loopback httptest
// listener that plays a script of replies (status, headers, error body, or
// SSE frames, including recorded fixtures), records every request body,
// and takes per-frame latency from an injected clock. Test-only package.
package providertest

import (
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/provider"
)

// Reply is one scripted response.
type Reply struct {
	Status int               // 0 = 200
	Header map[string]string // e.g. Retry-After
	Body   string            // non-stream body (errors)
	Frames []string          // SSE data payloads, each sent as "data: <f>\n\n"
	Raw    string            // SSE text replayed verbatim (recorded fixture)
	Delay  time.Duration     // clock latency before every frame
	Hang   bool              // keep the stream open until the client leaves
}

// Clock supplies latency; FakeClock makes it deterministic.
type Clock interface {
	After(d time.Duration) <-chan time.Time
}

// RealClock is the wall clock.
type RealClock struct{}

// After is time.After.
func (RealClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Server is a running fake provider.
type Server struct {
	URL   string // base URL for provider.Route.BaseURL
	srv   *httptest.Server
	clock Clock
	tb    testing.TB

	mu     sync.Mutex
	script []Reply
	reqs   [][]byte
}

// New starts a server playing script with clock (nil = RealClock); it is
// closed when tb ends. A request beyond the script fails the test.
func New(tb testing.TB, clock Clock, script ...Reply) *Server {
	tb.Helper()
	if clock == nil {
		clock = RealClock{}
	}
	s := &Server{clock: clock, tb: tb, script: script}
	s.srv = httptest.NewServer(http.HandlerFunc(s.serve))
	s.URL = s.srv.URL + "/v1"
	tb.Cleanup(s.srv.Close)
	return s
}

// Push appends replies to the script.
func (s *Server) Push(r ...Reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.script = append(s.script, r...)
}

// Requests returns the request bodies received so far.
func (s *Server) Requests() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.reqs...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	s.mu.Lock()
	s.reqs = append(s.reqs, body)
	var rep Reply
	ok := len(s.script) > 0
	if ok {
		rep, s.script = s.script[0], s.script[1:]
	}
	s.mu.Unlock()
	if err != nil || !ok || r.URL.Path != "/v1/chat/completions" {
		s.tb.Errorf("providertest: unexpected request %s %s (script left: %v, read: %v)", r.Method, r.URL.Path, ok, err)
		http.Error(w, `{"error":{"code":"providertest_script_exhausted"}}`, http.StatusTeapot)
		return
	}
	for k, v := range rep.Header {
		w.Header().Set(k, v)
	}
	if rep.Status != 0 && rep.Status != http.StatusOK {
		w.WriteHeader(rep.Status)
		if _, err := io.WriteString(w, rep.Body); err != nil {
			s.tb.Logf("providertest: write error body: %v", err)
		}
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	s.stream(w, r, &rep)
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request, rep *Reply) {
	rc := http.NewResponseController(w)
	chunks := make([]string, 0, len(rep.Frames)+1)
	for _, f := range rep.Frames {
		chunks = append(chunks, "data: "+f+"\n\n")
	}
	if rep.Raw != "" {
		chunks = append(chunks, rep.Raw)
	}
	for _, c := range chunks {
		select {
		case <-s.clock.After(rep.Delay):
		case <-r.Context().Done():
			return
		}
		if _, err := io.WriteString(w, c); err != nil {
			return
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
	if rep.Hang {
		<-r.Context().Done()
	}
}

// Text is a 200 stream of content deltas ending with finish stop and a
// usage chunk.
func Text(parts ...string) Reply {
	var r Reply
	for _, p := range parts {
		r.Frames = append(r.Frames, fmt.Sprintf(`{"choices":[{"index":0,"delta":{"content":%q}}]}`, p))
	}
	r.Frames = append(r.Frames, `{"choices":[{"index":0,"delta":{},"finish_reason":"`+provider.FinishStop+`"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":10}}`, "[DONE]")
	return r
}

// Status is an error reply with an OpenAI error envelope.
func Status(code int, errCode, param string) Reply {
	return Reply{Status: code, Body: fmt.Sprintf(`{"error":{"message":"fake","type":"invalid_request_error","code":%q,"param":%q}}`, errCode, param)}
}

// Fixture replays a recorded SSE stream from path.
func Fixture(tb testing.TB, path string) Reply {
	tb.Helper()
	b, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		tb.Fatal(err)
	}
	return Reply{Raw: strings.ReplaceAll(string(b), "\r\n", "\n")}
}

// fireBuffer is the capacity of a FakeClock channel: one tick, never blocks.
const fireBuffer = 1

// FakeClock is a manual clock: After channels fire only on Advance.
type FakeClock struct {
	mu      sync.Mutex
	cond    *sync.Cond
	now     time.Duration
	waiters []fakeWaiter
}

type fakeWaiter struct {
	at time.Duration
	ch chan time.Time
}

// NewFakeClock returns a clock at offset 0.
func NewFakeClock() *FakeClock {
	c := &FakeClock{}
	c.cond = sync.NewCond(&c.mu)
	return c
}

// After fires once the clock has advanced by d.
func (c *FakeClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, fireBuffer)
	c.mu.Lock()
	defer c.mu.Unlock()
	if d <= 0 {
		ch <- time.Time{}
		return ch
	}
	c.waiters = append(c.waiters, fakeWaiter{at: c.now + d, ch: ch})
	c.cond.Broadcast()
	return ch
}

// Advance moves the clock and fires due waiters.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now += d
	kept := c.waiters[:0]
	for _, w := range c.waiters {
		if w.at <= c.now {
			w.ch <- time.Time{}
			continue
		}
		kept = append(kept, w)
	}
	c.waiters = kept
}

// BlockUntil waits until n After calls are pending (the server is waiting
// on the clock), so a test can Advance deterministically.
func (c *FakeClock) BlockUntil(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.waiters) < n {
		c.cond.Wait()
	}
}
