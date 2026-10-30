package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/gateway"
)

// A CLI command must not open a second write connection while serve owns the
// data-dir lease. It publishes a JSON request below run/spool; serve (the lease
// holder) applies it: a send is enqueued into the outbox, a chat runs through
// admission and the turn loop with its answer written to run/chat/<id>.jsonl.
const sendSpoolPoll = 200 * time.Millisecond

// spoolRequest is one CLI request. Op is "send" (default for an older writer)
// or "chat"; ID names the reply file of a chat request.
type sendSpoolRequest struct {
	Op       string `json:"op,omitempty"`
	Platform string `json:"platform"`
	Chat     string `json:"chat"`
	Thread   string `json:"thread,omitempty"`
	Sender   string `json:"sender,omitempty"`
	Text     string `json:"text"`
	Key      string `json:"key,omitempty"`
	ID       string `json:"id,omitempty"`
}

func spoolDir(dataDir string) string { return filepath.Join(dataDir, "run", "spool") }

func queueSend(dataDir string, req *sendSpoolRequest) error {
	return queueSpool(spoolDir(dataDir), "send spool", "send", req)
}

// queueSpool writes one 0600 <kind>-<pid>-<nano>.json request under dir (0700).
func queueSpool(dir, label, kind string, req any) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	b, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	name := fmt.Sprintf("%s-%d-%d.json", kind, os.Getpid(), time.Now().UnixNano())
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

func (g *serveGateway) sendControlLoop(ctx context.Context) {
	tick := time.NewTicker(sendSpoolPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		g.drainSendSpool(ctx)
	}
}

func (g *serveGateway) drainSendSpool(ctx context.Context) {
	dir := spoolDir(g.dataDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "send-") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		b, aerr := os.ReadFile(path) // #nosec G304 -- path is <data-dir>/run/spool/<send-pid-nano>.json under the 0700 spool dir; the name comes from ReadDir, so it cannot carry a separator
		var req sendSpoolRequest
		if aerr == nil {
			aerr = json.Unmarshal(b, &req)
		}
		if aerr == nil {
			switch req.Op {
			case "", "send":
				aerr = g.enqueueSend(ctx, &req)
			case "chat":
				aerr = g.startChat(ctx, &req)
			default:
				aerr = fmt.Errorf("unknown request %s", cleanArg(req.Op))
			}
		}
		if aerr != nil {
			sessionsPrintf(g.stderr, "gateway: spool: %s\n", cleanLine(aerr.Error()))
		}
		if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			sessionsPrintf(g.stderr, "gateway: spool: %s\n", cleanLine(rerr.Error()))
		}
	}
}

func (g *serveGateway) enqueueSend(ctx context.Context, req *sendSpoolRequest) error {
	if req.Platform == "" || req.Chat == "" || req.Key == "" || req.Text == "" {
		return errors.New("platform, chat, key and text are required")
	}
	_, _, err := g.store.EnqueueDelivery(ctx, &gwstore.Delivery{
		IdemKey: gateway.DeliveryKey(req.Platform, req.Chat, req.Key), Platform: req.Platform,
		ChatID: req.Chat, ThreadID: req.Thread, Body: req.Text, Status: gateway.StatusQueued,
	})
	if err != nil {
		return fmt.Errorf("send spool: %w", err)
	}
	return nil
}

func sendDataDir(raw string) (string, error) {
	if raw != "" {
		return raw, nil
	}
	loader, err := config.New(config.OS())
	if err != nil {
		return "", fmt.Errorf("config: %w", err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return "", fmt.Errorf("config paths: %w", err)
	}
	return paths.DataDir, nil
}
