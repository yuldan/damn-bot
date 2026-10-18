package h2h

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/bench/evals"
)

// TimeoutHold adalah lama koneksi ditahan untuk fault "timeout" sebelum ditutup tanpa respons.
const TimeoutHold = 30 * time.Second

// PageServer menyajikan Task.Pages ({{BASE_URL}}) dan menyuntik Task.Faults HTTP:
// target gagal Count kali pertama, lalu pulih.
//   - timeout: koneksi ditahan TimeoutHold lalu ditutup tanpa respons;
//   - http_500: status 500 dengan body teks;
//   - malformed_json: body JSON terpotong di tengah.
type PageServer struct {
	pages  map[string]string
	mu     sync.Mutex
	faults map[string]*evals.Fault // salinan; Count berkurang tiap kegagalan
	hits   []Hit
}

// Hit adalah satu request ke server halaman (ikut di hasil task untuk audit).
type Hit struct {
	Path   string `json:"path"`
	Status int    `json:"status"`
	Fault  string `json:"fault,omitempty"`
}

// NewPageServer membuat server untuk satu task.
func NewPageServer(t *evals.Task) *PageServer {
	s := &PageServer{pages: t.Pages, faults: map[string]*evals.Fault{}}
	for _, f := range t.Faults {
		if strings.HasPrefix(f.Target, "/") {
			s.faults[f.Target] = &evals.Fault{Target: f.Target, Kind: f.Kind, Count: f.Count}
		}
	}
	return s
}

// Hits mengembalikan salinan log request.
func (s *PageServer) Hits() []Hit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Hit(nil), s.hits...)
}

func (s *PageServer) log(h Hit) {
	s.mu.Lock()
	s.hits = append(s.hits, h)
	s.mu.Unlock()
}

func (s *PageServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	body, ok := s.pages[path]
	if !ok {
		s.log(Hit{Path: path, Status: http.StatusNotFound})
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	var kind string
	if f := s.faults[path]; f != nil && f.Count > 0 {
		f.Count--
		kind = f.Kind
	}
	s.mu.Unlock()

	ctype := "text/html; charset=utf-8"
	if strings.HasSuffix(path, ".json") {
		ctype = "application/json"
	}
	switch kind {
	case "timeout":
		s.log(Hit{Path: path, Fault: kind})
		select {
		case <-time.After(TimeoutHold):
		case <-r.Context().Done():
		}
		if hj, ok := w.(http.Hijacker); ok {
			if c, _, err := hj.Hijack(); err == nil {
				_ = c.Close()
			}
		}
	case "http_500":
		s.log(Hit{Path: path, Status: http.StatusInternalServerError, Fault: kind})
		http.Error(w, "internal server error", http.StatusInternalServerError)
	case "malformed_json":
		s.log(Hit{Path: path, Status: http.StatusOK, Fault: kind})
		w.Header().Set("Content-Type", ctype)
		_, _ = w.Write([]byte(body[:len(body)/2]))
	default:
		s.log(Hit{Path: path, Status: http.StatusOK})
		w.Header().Set("Content-Type", ctype)
		_, _ = w.Write([]byte(body))
	}
}
