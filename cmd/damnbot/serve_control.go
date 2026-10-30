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

// The lease holder applies CLI requests from run/spool (0700/0600).
const controlPoll = 200 * time.Millisecond

type spoolRequest struct {
	Op       string `json:"op"`
	Platform string `json:"platform,omitempty"`
	Chat     string `json:"chat,omitempty"`
	Thread   string `json:"thread,omitempty"`
	Text     string `json:"text,omitempty"`
	Key      string `json:"key,omitempty"`
	Name     string `json:"name,omitempty"`
}

func spoolDir(dataDir string) string { return filepath.Join(dataDir, "run", "spool") }

func queueRequest(dataDir string, req spoolRequest) error {
	dir := spoolDir(dataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%d-%d.json", os.Getpid(), time.Now().UnixNano())
	return os.WriteFile(filepath.Join(dir, name), b, 0o600)
}

func readPlatforms(dataDir string) []gateway.AdapterStatus {
	b, err := os.ReadFile(filepath.Join(dataDir, "run", "platforms.json")) //nolint:gosec
	if err != nil {
		return nil
	}
	var list []gateway.AdapterStatus
	_ = json.Unmarshal(b, &list)
	return list
}

func dataDirFor(raw string) (string, error) {
	if raw != "" {
		return raw, nil
	}
	loader, err := config.New(config.OS())
	if err != nil {
		return "", err
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return "", err
	}
	return paths.DataDir, nil
}

func (g *serveGateway) controlLoop(ctx context.Context) {
	tick := time.NewTicker(controlPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if g.drainSpool(ctx) {
			g.writePlatforms()
		}
	}
}

func (g *serveGateway) drainSpool(ctx context.Context) bool {
	dir := spoolDir(g.dataDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	platform := false
	for _, name := range names {
		path := filepath.Join(dir, name)
		b, aerr := os.ReadFile(path) //nolint:gosec // inside the 0700 spool
		var req spoolRequest
		if aerr == nil {
			aerr = json.Unmarshal(b, &req)
		}
		seen := false
		if aerr == nil {
			switch req.Op {
			case "send":
				aerr = g.enqueueSpool(ctx, req)
			case "platform_pause", "platform_resume":
				_, aerr = g.platformAction(ctx, req.Name, req.Op == "platform_pause")
				seen = true
			default:
				aerr = errors.New("unknown request " + cleanArg(req.Op))
			}
		}
		platform = platform || seen
		if aerr != nil {
			sessionsPrintf(g.stderr, "gateway: spool: %s\n", cleanLine(aerr.Error()))
		}
		if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			sessionsPrintf(g.stderr, "gateway: spool: %s\n", cleanLine(rerr.Error()))
		}
	}
	return platform
}

func (g *serveGateway) enqueueSpool(ctx context.Context, req spoolRequest) error {
	if req.Platform == "" || req.Chat == "" || req.Key == "" || req.Text == "" {
		return errors.New("platform, chat, key and text are required")
	}
	_, _, err := g.store.EnqueueDelivery(ctx, &gwstore.Delivery{
		IdemKey: gateway.DeliveryKey(req.Platform, req.Chat, req.Key), Platform: req.Platform,
		ChatID: req.Chat, ThreadID: req.Thread, Body: req.Text, Status: gateway.StatusQueued,
	})
	return err
}

func (g *serveGateway) writePlatforms() {
	if g.registry == nil {
		return
	}
	b, err := json.Marshal(g.registry.Status())
	if err != nil {
		return
	}
	if err := os.WriteFile(filepath.Join(g.dataDir, "run", "platforms.json"), b, 0o600); err != nil {
		sessionsPrintf(g.stderr, "gateway: platform status: %s\n", cleanLine(err.Error()))
	}
}

func serveLockHeld(dataDir string) bool {
	path := filepath.Join(dataDir, "run", "serve.lock")
	if _, err := os.Stat(path); err != nil {
		return false
	}
	f, err := tryFlock(path)
	if err != nil {
		return errors.Is(err, errFlockBusy)
	}
	_ = unlockFlock(f) //nolint:errcheck // best effort probe
	return false
}
