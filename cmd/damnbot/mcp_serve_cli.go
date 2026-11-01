//go:build !minimal

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/ctxdb/mcpservestore"
	"github.com/damnbot/damnbot/internal/mcp"
	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/netx/jsonrpc"
)

// mcpServeTokenEnv carries the stdio bearer: never argv, never a flag, so the
// token does not appear in a process list.
const mcpServeTokenEnv = "DAMNBOT_MCP_TOKEN"

// serveMaxFrame bounds one MCP request frame (SPEC §A.6 16 MiB).
const serveMaxFrame = 16 << 20

// mcpServe implements `damnbot mcp serve` (SEC-21/GAP-SEC-21): a local MCP
// server exposing read-only damnbot tools, a send tool behind approval, and
// the cross-process approval inbox. The default transport is stdio; --listen
// adds the Streamable HTTP subset so a client can carry a bearer header and
// receive a real 401/403 instead of an in-band error.
func mcpServe(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("mcp serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", "", "loopback host:port for Streamable HTTP; empty uses stdio")
	dataDir := fs.String("data-dir", "", "data directory (default: profile data dir)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		if fs.Arg(0) == "token" {
			return mcpServeToken(fs.Args()[1:], stdout, stderr)
		}
		return mcpUsageErr(stderr, "serve takes no positional arguments")
	}
	loader, err := config.New(config.OS())
	if err != nil {
		return mcpFail(stderr, "serve", err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return mcpFail(stderr, "serve", err)
	}
	dir := *dataDir
	if dir == "" {
		dir = paths.DataDir
	}
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "mcp_serve"})
	if err != nil {
		return mcpFail(stderr, "serve", fmt.Errorf("open database: %w", err))
	}
	defer mcpCloseDB(db, stderr)
	store := mcpservestore.Store{DB: db}
	cfg, cerr := loader.Load(paths.ConfigFile())
	if cerr != nil && !errors.Is(cerr, os.ErrNotExist) {
		return mcpFail(stderr, "serve", cerr)
	}
	tools := mcpServeTools(db, dir, &cfg)
	// Standalone serve has no interactive owner channel: non-read tools are
	// denied rather than silently approved. A daemon host can inject a real
	// approver through the same seam.
	srv, err := mcp.NewServe(mcp.ServeConfig{
		Tokens: serveTokenStore{store: store}, Profile: activeProfile(), Approvals: host.DenyApprovals{}, Tools: tools,
	})
	if err != nil {
		return mcpFail(stderr, "serve", err)
	}
	if *listen != "" {
		return mcpServeHTTP(*listen, srv, stdout, stderr)
	}
	token := config.OS().Getenv(mcpServeTokenEnv)
	tok, err := srv.Authenticate(context.Background(), token)
	if err != nil {
		sessionsPrintf(stderr, "mcp serve: invalid %s\n", mcpServeTokenEnv)
		return 1
	}
	return mcpServeStdio(srv.Conn(tok), stdout, stderr)
}

// serveTokenStore adapts mcpservestore to internal/mcp (no module package
// dependency in either direction).
type serveTokenStore struct{ store mcpservestore.Store }

func (s serveTokenStore) Authenticate(ctx context.Context, rawToken, profile string) (mcp.ServeToken, error) {
	t, err := s.store.Authenticate(ctx, rawToken, profile)
	if err != nil {
		return mcp.ServeToken{}, err
	}
	return mcp.ServeToken{IDHash: t.IDHash, Profile: t.Profile, Identity: t.Identity, Scope: t.Scope}, nil
}

// mcpServeStdio speaks newline/Content-Length framed JSON-RPC on stdin/stdout
// and exits when the client closes the pipe.
func mcpServeStdio(conn *mcp.ServeConn, stdout, stderr io.Writer) int {
	reader := &eofReader{r: stdin(), done: make(chan struct{})}
	c := jsonrpc.NewConn(reader, stdout, nil, jsonrpc.Options{Handler: conn.Handler, MaxFrame: serveMaxFrame})
	defer func() {
		if err := c.Close(); err != nil {
			sessionsPrintf(stderr, "mcp serve: close: %s\n", cleanLine(err.Error()))
		}
	}()
	ctx, cancel := signalContext()
	defer cancel()
	select {
	case <-reader.done:
		return 0
	case <-ctx.Done():
		return 0
	}
}

// eofReader closes done on the first read error, so the server exits when the
// client ends the conversation.
type eofReader struct {
	r    io.Reader
	done chan struct{}
	once sync.Once
}

func (r *eofReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if err != nil {
		r.once.Do(func() { close(r.done) })
	}
	return n, err
}

// mcpServeHTTP serves the Streamable HTTP subset on a loopback listener:
// bearer auth (401), scope (403), rate limit (429), and one JSON response per
// request. The token is never echoed.
func mcpServeHTTP(addr string, srv *mcp.Serve, stdout, stderr io.Writer) int {
	hostPart, port, err := net.SplitHostPort(addr)
	if err != nil {
		return mcpFail(stderr, "serve", fmt.Errorf("--listen must be host:port"))
	}
	if hostPart == "" || hostPart == "0.0.0.0" || hostPart == "::" {
		return mcpFail(stderr, "serve", errors.New("--listen must bind a loopback address"))
	}
	if ip := net.ParseIP(hostPart); ip == nil || !ip.IsLoopback() {
		return mcpFail(stderr, "serve", errors.New("--listen must bind a loopback address"))
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(hostPart, port))
	if err != nil {
		return mcpFail(stderr, "serve", err)
	}
	sessionsPrintf(stderr, "mcp serve: listening on http://%s\n", ln.Addr())
	httpSrv := &http.Server{Handler: &serveHTTP{srv: srv}, ReadHeaderTimeout: 10 * time.Second}
	ctx, cancel := signalContext()
	defer cancel()
	go func() {
		<-ctx.Done()
		shutdown, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = httpSrv.Shutdown(shutdown) //nolint:errcheck // best-effort drain
	}()
	if serr := httpSrv.Serve(ln); serr != nil && !errors.Is(serr, http.ErrServerClosed) {
		return mcpFail(stderr, "serve", serr)
	}
	return 0
}

type serveHTTP struct{ srv *mcp.Serve }

func (h *serveHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := bearerToken(r.Header.Get("Authorization"))
	tok, err := h.srv.Authenticate(r.Context(), token)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="damnbot mcp serve"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, serveMaxFrame))
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	var msg jsonrpc.Message
	if err := json.Unmarshal(body, &msg); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	result, rerr := h.srv.Conn(tok).Handler(r.Context(), msg.Method, msg.Params)
	if len(bytes.TrimSpace(msg.ID)) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	reply := jsonrpc.Message{JSONRPC: jsonrpc.Version, ID: msg.ID}
	status := http.StatusOK
	switch {
	case rerr != nil:
		reply.Error = rerr
		switch rerr.Code {
		case mcp.ServeCodeUnauthorized:
			status = http.StatusUnauthorized
		case mcp.ServeCodeForbidden:
			status = http.StatusForbidden
		case mcp.ServeCodeRateLimited:
			status = http.StatusTooManyRequests
		}
	default:
		raw, merr := json.Marshal(result)
		if merr != nil {
			http.Error(w, "encode", http.StatusInternalServerError)
			return
		}
		reply.Result = raw
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(reply) //nolint:errcheck // response write
}

// signalContext is the SIGINT/SIGTERM lifetime of the serve command.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

// mcpServeTools builds the tool table. Every tool is read-only unless the
// token scope and the approval gate say otherwise.
func mcpServeTools(db *ctxdb.Store, dataDir string, cfg *config.Config) []mcp.ServeTool {
	return []mcp.ServeTool{
		{
			Name: "recall", Scope: mcp.ServeScopeRead, Effect: host.EffectRead,
			Description: "Search past conversations in this profile.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","maxLength":512},"limit":{"type":"integer","minimum":1,"maximum":50}},"required":["query"],"additionalProperties":false}`),
			Run: func(ctx context.Context, args json.RawMessage) (string, error) {
				var in struct {
					Query string `json:"query"`
					Limit int    `json:"limit"`
				}
				if err := json.Unmarshal(args, &in); err != nil || in.Query == "" {
					return "", errors.New("recall needs a query")
				}
				hits, err := db.Recall(ctx, in.Query, in.Limit)
				if err != nil {
					return "", err
				}
				out, _ := json.Marshal(hits)
				return string(out), nil
			},
		},
		{
			Name: "sessions_list", Scope: mcp.ServeScopeRead, Effect: host.EffectRead,
			Description: "List visible session ids.",
			Schema:      json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
				ids, err := db.ListSessions(ctx)
				if err != nil {
					return "", err
				}
				sort.Strings(ids)
				out, _ := json.Marshal(ids)
				return string(out), nil
			},
		},
		{
			Name: "sessions_read", Scope: mcp.ServeScopeRead, Effect: host.EffectRead,
			Description: "Read the last events of one session.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"session_id":{"type":"string","maxLength":64},"limit":{"type":"integer","minimum":1,"maximum":100}},"required":["session_id"],"additionalProperties":false}`),
			Run: func(ctx context.Context, args json.RawMessage) (string, error) {
				var in struct {
					SessionID string `json:"session_id"`
					Limit     int    `json:"limit"`
				}
				if err := json.Unmarshal(args, &in); err != nil || in.SessionID == "" {
					return "", errors.New("sessions_read needs session_id")
				}
				events, err := db.HistoryTail(ctx, in.SessionID, in.Limit)
				if err != nil {
					return "", err
				}
				type row struct {
					Seq, At int64
					Kind    string `json:"kind"`
					Role    string `json:"role,omitempty"`
					Content string `json:"content"`
				}
				rows := make([]row, 0, len(events))
				for _, ev := range events {
					rows = append(rows, row{Seq: ev.Seq, At: ev.CreatedAt, Kind: ev.Kind, Role: ev.Role, Content: truncate(ev.Content, 4000)})
				}
				out, _ := json.Marshal(rows)
				return string(out), nil
			},
		},
		{
			Name: "messages_send", Scope: mcp.ServeScopeSend, Effect: host.EffectNonIdempotent,
			Description: "Queue one message to the owner or an allowlisted chat (needs approval).",
			Schema:      json.RawMessage(`{"type":"object","properties":{"target":{"type":"string","maxLength":128},"text":{"type":"string","maxLength":4000},"idempotency_key":{"type":"string","maxLength":128}},"required":["target","text"],"additionalProperties":false}`),
			Preview: func(args json.RawMessage) string {
				return "send_message " + truncate(string(args), host.MaxApprovalPreview)
			},
			Run: func(ctx context.Context, args json.RawMessage) (string, error) {
				return mcpSendMessage(ctx, db, dataDir, args)
			},
		},
		{
			Name: "permissions_list_open", Scope: mcp.ServeScopeApprove, Effect: host.EffectRead,
			Description: "List approvals waiting in the journal.",
			Schema:      json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer","minimum":1,"maximum":200}},"additionalProperties":false}`),
			Run: func(ctx context.Context, args json.RawMessage) (string, error) {
				var in struct {
					Limit int `json:"limit"`
				}
				_ = json.Unmarshal(args, &in) //nolint:errcheck // optional
				store := mcpservestore.Store{DB: db}
				rows, err := store.OpenApprovals(ctx, in.Limit)
				if err != nil {
					return "", err
				}
				out, _ := json.Marshal(rows)
				return string(out), nil
			},
		},
		{
			Name: "permissions_respond", Scope: mcp.ServeScopeApprove, Effect: host.EffectRead,
			Description: "Answer one pending approval (allow_once or deny).",
			Schema:      json.RawMessage(`{"type":"object","properties":{"request_id":{"type":"string","maxLength":128},"decision":{"type":"string","enum":["allow_once","deny"]},"preview_sha":{"type":"string","maxLength":64}},"required":["request_id","decision","preview_sha"],"additionalProperties":false}`),
			Run: func(ctx context.Context, args json.RawMessage) (string, error) {
				var in struct {
					RequestID  string `json:"request_id"`
					Decision   string `json:"decision"`
					PreviewSHA string `json:"preview_sha"`
				}
				if err := json.Unmarshal(args, &in); err != nil {
					return "", errors.New("permissions_respond needs request_id, decision, preview_sha")
				}
				tok, ok := mcp.TokenFromContext(ctx)
				if !ok {
					return "", errors.New("permissions_respond needs an authenticated token")
				}
				store := mcpservestore.Store{DB: db}
				row, err := store.Respond(ctx, mcpservestore.RespondRequest{
					RequestID: in.RequestID, Decision: in.Decision, PreviewSHA: in.PreviewSHA,
					Responder: tok.Identity, ResponderTokenHash: tok.IDHash, Now: time.Now().UnixMilli(),
				})
				if err != nil {
					return "", err
				}
				out, _ := json.Marshal(row)
				return string(out), nil
			},
		},
	}
}

// mcpSendMessage enqueues a durable delivery after the serve-side checks:
// scope+approval are already enforced by Serve; here the target must be the
// owner or an allowlisted platform:id and a live serve must own the outbox.
func mcpSendMessage(ctx context.Context, db *ctxdb.Store, dataDir string, args json.RawMessage) (string, error) {
	var in struct {
		Target         string `json:"target"`
		Text           string `json:"text"`
		IdempotencyKey string `json:"idempotency_key"`
	}
	if err := json.Unmarshal(args, &in); err != nil || in.Target == "" || in.Text == "" || len(in.Text) > 4000 {
		return "", errors.New("send needs target and text (<= 4000 chars)")
	}
	owner, allow, err := gatewayEnv()
	if err != nil {
		return "", errors.New("send unavailable: no gateway owner configured")
	}
	if !sendTargetAllowed(in.Target, owner, allow) {
		return "", errors.New("send target is not allowlisted")
	}
	if !mcpServeAlive(dataDir) {
		return "", errors.New("send unavailable: serve is not running")
	}
	platform, chat, ok := strings.Cut(in.Target, ":")
	if !ok {
		return "", errors.New("send target must be platform:id")
	}
	key := in.IdempotencyKey
	if key == "" {
		key = newIdemKey()
	}
	store := gwstore.Store{DB: db}
	id, inserted, err := store.EnqueueDelivery(ctx, &gwstore.Delivery{
		IdemKey: "mcp:" + key, Platform: platform, ChatID: chat, Body: in.Text,
	})
	if err != nil {
		return "", err
	}
	out, _ := json.Marshal(map[string]any{"delivery_id": id, "inserted": inserted})
	return string(out), nil
}

func sendTargetAllowed(target, owner string, allow []string) bool {
	if target == owner {
		return true
	}
	for _, item := range allow {
		if item == target {
			return true
		}
	}
	return false
}

func newIdemKey() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("mcp-%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// mcpServeAlive reports whether a `damnbot serve` holds the data dir lease.
func mcpServeAlive(dataDir string) bool {
	f, err := tryFlock(filepath.Join(dataDir, "run", "serve.lock"))
	if err != nil {
		return errors.Is(err, errFlockBusy)
	}
	_ = unlockFlock(f) //nolint:errcheck // probe only
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
