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

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/gateway"
)

// The lease holder applies CLI requests from run/spool (0700/0600) on the
// shared idle-backed-off poll loop (runPollLoop).

type platformRequest struct {
	Op       string `json:"op"`
	Platform string `json:"platform,omitempty"`
	Chat     string `json:"chat,omitempty"`
	Thread   string `json:"thread,omitempty"`
	Text     string `json:"text,omitempty"`
	Key      string `json:"key,omitempty"`
	Name     string `json:"name,omitempty"`
}

func queuePlatform(dataDir string, req *platformRequest) error {
	return queueSpool(spoolDir(dataDir), "platform spool", "platform", req)
}

func readPlatforms(dataDir string) []gateway.AdapterStatus {
	b, err := fs.ReadFile(os.DirFS(filepath.Join(dataDir, "run")), "platforms.json")
	if err != nil {
		return nil
	}
	var list []gateway.AdapterStatus
	if err := json.Unmarshal(b, &list); err != nil {
		return nil
	}
	return list
}

func platformDataDir(raw string) (string, error) {
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

// drainPlatformSpool applies every pending request and reports how many files
// it handled (the poll-loop pacing) and whether a platform changed (the status
// file rewrite).
func (g *serveGateway) drainPlatformSpool(ctx context.Context) (worked int, changed bool) {
	dir := spoolDir(g.dataDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, false
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "platform-") && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	for _, name := range names {
		worked++
		path := filepath.Join(dir, name)
		b, aerr := fs.ReadFile(os.DirFS(dir), name)
		var req platformRequest
		if aerr == nil {
			aerr = json.Unmarshal(b, &req)
		}
		seen := false
		if aerr == nil {
			switch req.Op {
			case "platform_pause", "platform_resume":
				_, aerr = g.platformAction(ctx, req.Name, req.Op == "platform_pause")
				seen = true
			default:
				aerr = errors.New("unknown request " + cleanArg(req.Op))
			}
		}
		changed = changed || seen
		if aerr != nil {
			sessionsPrintf(g.stderr, "gateway: spool: %s\n", cleanLine(aerr.Error()))
		}
		if rerr := os.Remove(path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			sessionsPrintf(g.stderr, "gateway: spool: %s\n", cleanLine(rerr.Error()))
		}
	}
	return worked, changed
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

func platformLockHeld(dataDir string) bool {
	path := filepath.Join(dataDir, "run", "serve.lock")
	if _, err := os.Stat(path); err != nil {
		return false
	}
	f, err := tryFlock(path)
	if err != nil {
		return errors.Is(err, errFlockBusy)
	}
	if uerr := unlockFlock(f); uerr != nil {
		return false
	}
	return false
}
