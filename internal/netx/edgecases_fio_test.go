package netx

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestSSEEncodingEdgeCases covers the EC-FIO streaming rows: the SSE body is
// always UTF-8 whatever the Content-Type says (EC-FIO-016) and the client
// advertises only the encodings it can decode (EC-FIO-017).
func TestSSEEncodingEdgeCases(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	cases := []struct {
		id string
		fn func(*testing.T)
	}{
		{"EC-FIO-016", ecFio016},
		{"EC-FIO-017", ecFio017},
	}
	for _, c := range cases {
		if g.Item(t, c.id, "F1", "covered") {
			t.Run(c.id, c.fn)
		}
	}
}

// firstEvent opens url and returns the first event's data.
func firstEvent(t *testing.T, url string) string {
	t.Helper()
	s := openStream(t, url, time.Minute, time.Minute)
	ev, err := s.Next()
	if err != nil {
		t.Fatalf("first event: %v", err)
	}
	return string(ev.Data)
}

// EC-FIO-016: CJK search text survives a charset= label the client must
// ignore (the SSE stream is UTF-8 per the provider contract).
func ecFio016(t *testing.T) {
	const cjk = "検索テキスト"
	srv, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream; charset=iso-8859-1")
		_, _ = io.WriteString(w, "data: "+cjk+"\n\n")
	})
	if got := firstEvent(t, srv.URL); got != cjk {
		t.Fatalf("event = %q, want %q", got, cjk)
	}
}

// EC-FIO-017: the transport advertises gzip (which it decodes) and never
// brotli/zstd; a gzip-compressed SSE body still parses.
func ecFio017(t *testing.T) {
	accept := make(chan string, 1)
	srv, _ := server(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case accept <- r.Header.Get("Accept-Encoding"):
		default:
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Encoding", "gzip")
		zw := gzip.NewWriter(w)
		if _, err := io.WriteString(zw, "data: compressed\n\n"); err != nil {
			t.Error(err)
		}
		if err := zw.Close(); err != nil {
			t.Error(err)
		}
	})
	if got := firstEvent(t, srv.URL); got != "compressed" {
		t.Fatalf("gzip event = %q", got)
	}
	got := <-accept
	if !strings.Contains(got, "gzip") {
		t.Fatalf("Accept-Encoding = %q, want gzip", got)
	}
	for _, unsupported := range []string{"br", "zstd"} {
		if strings.Contains(got, unsupported) {
			t.Fatalf("Accept-Encoding = %q advertises unsupported %s", got, unsupported)
		}
	}
}
