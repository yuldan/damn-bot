//go:build !minimal

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider/auth"
)

const proxyDefaultListen = "127.0.0.1:8811"

// proxyRunState is <data_dir>/proxy/run.json: one running proxy. It carries
// no credential, only the loopback address and the provider name.
type proxyRunState struct {
	Addr      string `json:"addr"`
	Provider  string `json:"provider"`
	PID       int    `json:"pid"`
	StartedAt int64  `json:"started_at"`
}

// runProxy implements `damnbot proxy` (CLI-012, UI-022): a loopback
// OpenAI-compatible endpoint in front of one OAuth provider.
func runProxy(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("proxy", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config path")
	dataDir := fs.String("data-dir", "", "data directory (default: profile data dir)")
	if err := fs.Parse(args); err != nil || fs.NArg() == 0 {
		return f8ExitUsage
	}
	rest := fs.Args()
	dir, err := f8Dir(*dataDir)
	if err != nil {
		return f8CmdFail(stderr, "proxy", err)
	}
	switch rest[0] {
	case "start":
		return proxyStart(rest[1:], *cfgPath, dir, stdout, stderr)
	case "status":
		if len(rest) != 1 {
			return f8ExitUsage
		}
		return proxyStatus(dir, stdout, stderr)
	case "providers":
		if len(rest) != 1 {
			return f8ExitUsage
		}
		return proxyProviders(*cfgPath, dir, stdout, stderr)
	default:
		say(stderr, "proxy: unknown subcommand %q\n", cleanArg(rest[0]))
		return f8ExitUsage
	}
}

func proxyUsage(w io.Writer) {
	say(w, "usage: damnbot proxy [--config FILE] [--data-dir DIR] start --provider NAME [--listen 127.0.0.1:8811] [--once]\n")
	say(w, "       damnbot proxy status\n")
	say(w, "       damnbot proxy providers\n")
}

// proxyProviders lists the custom providers that hold an OAuth token in the
// credential store. Only names and counts are printed.
func proxyProviders(cfgPath, dataDir string, stdout, stderr io.Writer) int {
	cfg, _, _, err := f8LoadSetup(cfgPath, dataDir)
	if err != nil {
		return f8CmdFail(stderr, "proxy", err)
	}
	store, _ := openProxyTokenStore(dataDir)
	ctx := context.Background()
	for i := range cfg.Providers.Custom {
		p := &cfg.Providers.Custom[i]
		state := "missing"
		if store != nil {
			if _, lerr := store.Load(ctx, p.Name); lerr == nil {
				state = "oauth"
			}
		}
		say(stdout, "provider %s api=%s models=%d auth=%s\n", cleanLine(p.Name), cleanLine(p.API), len(p.Models), state)
	}
	if len(cfg.Providers.Custom) == 0 {
		say(stdout, "providers none\n")
	}
	return f8ExitOK
}

// proxyStatus probes the loopback health endpoint named by run.json. A stale
// file is reported as stopped and removed.
func proxyStatus(dataDir string, stdout, stderr io.Writer) int {
	statePath := filepath.Join(dataDir, "proxy", "run.json")
	var st proxyRunState
	if err := f8Load(statePath, &st); err != nil {
		return f8CmdFail(stderr, "proxy", err)
	}
	if st.Addr == "" {
		say(stdout, "proxy stopped\n")
		return f8ExitOK
	}
	if !proxyHealthy(st.Addr) {
		if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return f8CmdFail(stderr, "proxy", err)
		}
		say(stdout, "proxy stopped\n")
		return f8ExitOK
	}
	say(stdout, "proxy running addr=%s provider=%s pid=%d\n", cleanLine(st.Addr), cleanLine(st.Provider), st.PID)
	return f8ExitOK
}

func proxyHealthy(addr string) bool {
	client := netx.LoopbackClient()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// proxyStart serves until a signal, or until one completion when --once is
// set. The provider token is read from the credential store; it is never
// printed, logged, or written to run.json.
func proxyStart(args []string, cfgPath, dataDir string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("proxy start", flag.ContinueOnError)
	fs.SetOutput(stderr)
	providerName := fs.String("provider", "", "custom provider with a stored OAuth token (required)")
	listen := fs.String("listen", proxyDefaultListen, "loopback listener")
	once := fs.Bool("once", false, "serve one completion then exit")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		proxyUsage(stderr)
		return f8ExitUsage
	}
	if *providerName == "" {
		proxyUsage(stderr)
		return f8ExitUsage
	}
	addr, err := proxyListenAddr(*listen)
	if err != nil {
		return f8CmdFail(stderr, "proxy start", err)
	}
	cfg, _, _, err := f8LoadSetup(cfgPath, dataDir)
	if err != nil {
		return f8CmdFail(stderr, "proxy start", err)
	}
	prov, err := proxyFindProvider(&cfg, *providerName)
	if err != nil {
		return f8CmdFail(stderr, "proxy start", err)
	}
	store, err := openProxyTokenStore(dataDir)
	if err != nil {
		return f8CmdFail(stderr, "proxy start", err)
	}
	tok, err := store.Load(context.Background(), prov.Name)
	if err != nil || tok.AccessToken == "" {
		return f8CmdFail(stderr, "proxy start", f8Unavail("proxy",
			"provider "+cleanArg(prov.Name)+" has no stored OAuth token; run `damnbot setup auth login "+cleanArg(prov.Name)+"`"))
	}
	if tok.Expired(time.Now()) {
		return f8CmdFail(stderr, "proxy start", f8Unavail("proxy",
			"the OAuth token for "+cleanArg(prov.Name)+" is expired; run `damnbot setup auth login "+cleanArg(prov.Name)+"`"))
	}
	base := strings.TrimRight(strings.TrimSpace(prov.BaseURL), "/")
	if err := proxyEndpointOK(base); err != nil {
		return f8CmdFail(stderr, "proxy start", err)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return f8CmdFail(stderr, "proxy start", fmt.Errorf("listen: %w", err))
	}
	defer func() { _ = ln.Close() }()
	st := proxyRunState{Addr: ln.Addr().String(), Provider: prov.Name, PID: os.Getpid(), StartedAt: time.Now().Unix()}
	statePath := filepath.Join(dataDir, "proxy", "run.json")
	if err := f8Save(statePath, &st); err != nil {
		return f8CmdFail(stderr, "proxy start", err)
	}
	defer func() { _ = os.Remove(statePath) }()

	srv := &http.Server{
		Handler:           proxyHandler(cfg, prov, tok.AccessToken, ln.Addr().String()),
		ReadHeaderTimeout: 10 * time.Second,
	}
	say(stdout, "proxy listening addr=%s provider=%s models=%d\n", cleanLine(st.Addr), cleanLine(prov.Name), len(prov.Models))
	done := make(chan struct{})
	if *once {
		srv.Handler = onceHandler(srv.Handler, done)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	if *once {
		select {
		case <-done:
		case err = <-serveErr:
			if !errors.Is(err, http.ErrServerClosed) {
				return f8CmdFail(stderr, "proxy start", err)
			}
		}
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			return f8CmdFail(stderr, "proxy start", err)
		}
		return f8ExitOK
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case <-ctx.Done():
	case err = <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return f8CmdFail(stderr, "proxy start", err)
		}
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil {
		return f8CmdFail(stderr, "proxy start", err)
	}
	return f8ExitOK
}

// onceHandler closes done after the first completion has been written.
func onceHandler(next http.Handler, done chan struct{}) http.Handler {
	var fired atomic.Bool
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/chat/completions") && fired.CompareAndSwap(false, true) {
			close(done)
		}
	})
}

// proxyHandler exposes /v1/models, /healthz and POST /v1/chat/completions.
// Every upstream call uses the inference egress point: the provider
// credential travels in the Authorization header, never in a body.
func proxyHandler(cfg config.Config, prov *config.CustomProvider, token, addr string) http.Handler {
	net := netx.New(netx.Config{Offline: cfg.Network.Offline, NoBackground: !cfg.Network.Background, Secrets: []string{token}})
	client := net.Client("inference", netx.Inference)
	upstream := strings.TrimRight(strings.TrimSpace(prov.BaseURL), "/") + "/chat/completions"
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok","provider":`+strconv.Quote(prov.Name)+`}`)
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		writeProxyJSON(w, proxyModels(prov))
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
		if err != nil || !json.Valid(body) {
			http.Error(w, `{"error":{"message":"invalid request body"}}`, http.StatusBadRequest)
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, upstream, strings.NewReader(string(body)))
		if err != nil {
			http.Error(w, `{"error":{"message":"invalid upstream"}}`, http.StatusBadGateway)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		if accept := r.Header.Get("Accept"); accept != "" {
			req.Header.Set("Accept", accept)
		}
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, `{"error":{"message":"provider unavailable"}}`, http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			http.Error(w, `{"error":{"message":"provider rejected the stored OAuth token"}}`, http.StatusBadGateway)
			return
		}
		proxyRelay(w, resp)
	})
	return mux
}

type proxyModelList struct {
	Object string       `json:"object"`
	Data   []proxyModel `json:"data"`
}

type proxyModel struct {
	ID     string `json:"id"`
	Object string `json:"object"`
}

func proxyModels(prov *config.CustomProvider) proxyModelList {
	data := make([]proxyModel, 0, len(prov.Models))
	for _, m := range prov.Models {
		data = append(data, proxyModel{ID: m.ID, Object: "model"})
	}
	return proxyModelList{Object: "list", Data: data}
}

func writeProxyJSON(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

// proxyRelay streams the upstream response: SSE chunks are flushed as they
// arrive, so a local client sees the provider's own pacing.
func proxyRelay(w http.ResponseWriter, resp *http.Response) {
	for _, key := range []string{"Content-Type", "Cache-Control"} {
		if v := resp.Header.Get(key); v != "" {
			w.Header().Set(key, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			return
		}
	}
}

// openProxyTokenStore opens the per-profile OAuth token store: keychain when
// usable, otherwise the 0600 file fallback below <data>/auth.
func openProxyTokenStore(dataDir string) (*auth.Store, error) {
	sec, serr := secretsStore()
	if serr != nil {
		sec = nil
	}
	store, err := auth.NewStore(sec, filepath.Join(dataDir, "auth"))
	if err != nil {
		return nil, fmt.Errorf("token store: %w", err)
	}
	return store, nil
}

func proxyFindProvider(cfg *config.Config, name string) (*config.CustomProvider, error) {
	for i := range cfg.Providers.Custom {
		if cfg.Providers.Custom[i].Name == name {
			p := &cfg.Providers.Custom[i]
			if p.API != "" && p.API != "openai-chat" {
				return nil, f8BadArg("provider %q speaks %s; the local proxy serves openai-chat", cleanArg(name), cleanArg(p.API))
			}
			if strings.TrimSpace(p.BaseURL) == "" {
				return nil, f8BadArg("provider %q has no base_url", cleanArg(name))
			}
			return p, nil
		}
	}
	return nil, f8BadArg("provider %q is not configured under [[providers.custom]]", cleanArg(name))
}

// proxyListenAddr accepts only loopback binds. Port 0 is allowed for tests
// and for callers that read the chosen port from run.json.
func proxyListenAddr(raw string) (string, error) {
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return "", f8BadArg("listen %q must be host:port", cleanArg(raw))
	}
	if host == "" || !proxyLoopback(host) {
		return "", f8BadArg("listen must be a loopback address")
	}
	if n, perr := strconv.Atoi(port); perr != nil || n < 0 || n > 65535 {
		return "", f8BadArg("listen port must be 0..65535")
	}
	return net.JoinHostPort(host, port), nil
}

func proxyLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func proxyEndpointOK(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return f8BadArg("provider base_url is not a URL")
	}
	if u.Scheme == "https" || (u.Scheme == "http" && proxyLoopback(u.Hostname())) {
		return nil
	}
	return f8BadArg("provider base_url must be https or a loopback http URL")
}
