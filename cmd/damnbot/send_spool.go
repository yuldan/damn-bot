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

// A CLI send must not open a second write connection while serve owns the
// data-dir lease. It publishes a JSON request below run/spool; serve (the lease
// holder) enqueues it into the outbox and its worker delivers it.
const sendSpoolPoll = 200 * time.Millisecond

type sendSpoolRequest struct {
	Platform string `json:"platform"`
	Chat     string `json:"chat"`
	Thread   string `json:"thread,omitempty"`
	Text     string `json:"text"`
	Key      string `json:"key"`
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
		b, aerr := os.ReadFile(path) //nolint:gosec // inside the 0700 spool
		var req sendSpoolRequest
		if aerr == nil {
			aerr = json.Unmarshal(b, &req)
		}
		if aerr == nil {
			aerr = g.enqueueSend(ctx, &req)
		}
		if aerr != nil {
			sessionsPrintf(g.stderr, "gateway: send spool: %s\n", cleanLine(aerr.Error()))
		}
		if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			sessionsPrintf(g.stderr, "gateway: send spool: %s\n", cleanLine(rerr.Error()))
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
