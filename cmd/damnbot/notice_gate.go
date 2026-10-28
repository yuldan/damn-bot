package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/tui"
)

// noticeGate persists the one-shot decision for each non-critical notice under
// <data_dir>/notices. One file per key makes the claim atomic across processes:
// O_CREATE|O_EXCL lets exactly one process show a tip, the rest see ErrExist.
// [ui] notices = false returns no gate at all, so nothing is read or written
// (GAP-OPS-09).
type noticeGate struct{ dir string }

// noticeGateFor returns nil when non-critical notices are disabled.
func noticeGateFor(cfg *config.Config, dataDir string) tui.NoticeGate {
	if !cfg.UI.Notices {
		return nil
	}
	return &noticeGate{dir: filepath.Join(dataDir, "notices")}
}

// Allow reports whether key may be shown now, and claims it when true.
func (g *noticeGate) Allow(key string) (bool, error) {
	if g == nil || key == "" {
		return false, errors.New("notice: empty key or gate")
	}
	if err := os.MkdirAll(g.dir, 0o700); err != nil {
		return false, fmt.Errorf("notice dir: %w", err)
	}
	sum := sha256.Sum256([]byte(key))
	path := filepath.Join(g.dir, hex.EncodeToString(sum[:16]))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- derived file name, fixed directory
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("notice claim: %w", err)
	}
	_, werr := fmt.Fprintf(f, "%s\n%s\n", key, time.Now().UTC().Format(time.RFC3339))
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		return false, fmt.Errorf("notice record: %w", err)
	}
	return true, nil
}
