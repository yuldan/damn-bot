package update

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
)

const (
	maxBinarySize = 64 << 20
	healthTimeout = 30 * time.Second
	updateDir     = "update"
	pendingFile   = "pending.json"
)

var (
	// ErrLocked means another updater holds <data>/update.lock.
	ErrLocked = errors.New("update: another update is in progress")
	// ErrNoPending means pending.json is missing, malformed, or points at a missing pair.
	ErrNoPending = errors.New("update: no usable pending rollback record")
	errNotNewer  = errors.New("update: target is not newer than the installed version")
)

// InstallOptions are the explicit inputs of an atomic install.
type InstallOptions struct {
	Client      *netx.Client
	FeedURL     string
	Channel     string
	Current     string
	PublicKey   ed25519.PublicKey
	DataDir     string
	BinaryPath  string
	HealthCheck func(context.Context, string) error // nil = doctor --quick
}

type pending struct {
	PrevBinary    string `json:"prev_binary"`
	PrevVersion   string `json:"prev_version"`
	TargetVersion string `json:"target_version"`
	PremigrateDB  string `json:"premigrate_db"`
	CreatedAt     string `json:"created_at"`
}

// Install downloads the signed asset, verifies checksum, health-checks it, and
// swaps it in with a rollback record. A failure never touches the running binary.
func Install(ctx context.Context, o *InstallOptions) (version string, err error) {
	if o.DataDir == "" || o.BinaryPath == "" {
		return "", errors.New("update: install needs data dir and binary path")
	}
	unlock, err := acquireLock(filepath.Join(o.DataDir, "update.lock"))
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	r, err := resolve(ctx, o.Client, o.FeedURL, o.Channel, o.PublicKey)
	if err != nil {
		return "", err
	}
	if newer, nerr := newerThan(r.man.Version, o.Current); nerr != nil {
		return "", fmt.Errorf("%w: %w", errNotNewer, nerr)
	} else if !newer {
		return "", errNotNewer
	}
	newPath, err := stage(ctx, o, &r)
	if err != nil {
		return "", err
	}
	p := pending{
		PrevBinary:    o.BinaryPath + ".prev",
		PrevVersion:   o.Current,
		TargetVersion: r.man.Version,
		PremigrateDB:  findPremigrate(o.DataDir),
		CreatedAt:     time.Now().UTC().Format(time.RFC3339),
	}
	if err := writePending(o.DataDir, &p); err != nil {
		return "", errors.Join(err, removeIfExists(newPath))
	}
	if err := swapBinary(o.BinaryPath, newPath); err != nil {
		return "", errors.Join(err, removePending(o.DataDir), removeIfExists(newPath))
	}
	return r.man.Version, nil
}

func stage(ctx context.Context, o *InstallOptions, r *resolved) (newPath string, err error) {
	newPath = filepath.Join(filepath.Dir(o.BinaryPath), "damnbot.new"+filepath.Ext(o.BinaryPath))
	tmp, err := os.CreateTemp(filepath.Dir(o.BinaryPath), ".damnbot-new-*")
	if err != nil {
		return "", fmt.Errorf("update: temp binary: %w", err)
	}
	defer func() { err = errors.Join(err, removeIfExists(tmp.Name())) }()
	if err := downloadBinary(ctx, o.Client, r.rel, r.asset, tmp); err != nil {
		return "", err
	}
	if err := removeIfExists(newPath); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), newPath); err != nil {
		return "", fmt.Errorf("update: publish new binary: %w", err)
	}
	health := o.HealthCheck
	if health == nil {
		health = quickHealth
	}
	if err := health(ctx, newPath); err != nil {
		return "", errors.Join(fmt.Errorf("update: health check failed; old binary untouched: %w", err), removeIfExists(newPath))
	}
	return newPath, nil
}

func downloadBinary(ctx context.Context, c *netx.Client, rel ghRelease, a asset, f *os.File) (err error) {
	rawURL, err := assetURL(rel, a.Name)
	if err != nil {
		return err
	}
	if verr := validateDownloadURL(rawURL); verr != nil {
		return verr
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return fmt.Errorf("update: build request: %w", err)
	}
	resp, err := c.DoOK(req)
	if err != nil {
		return fmt.Errorf("update: download: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("update: close download: %w", cerr)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxBinarySize+1))
	if err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = f.Chmod(0o755)
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("update: write binary: %w", err)
	}
	if n > maxBinarySize {
		return fmt.Errorf("update: binary exceeds %d bytes", maxBinarySize)
	}
	want, err := hex.DecodeString(a.SHA256)
	if err != nil || subtle.ConstantTimeCompare(h.Sum(nil), want) != 1 {
		return fmt.Errorf("update: binary sha256 does not match the signed manifest")
	}
	return nil
}

func quickHealth(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "damnbot", "doctor", "--quick")
	cmd.Path, cmd.Args[0], cmd.Err = path, path, nil
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("doctor --quick: %w", err)
	}
	return nil
}

func swapBinary(active, newPath string) error {
	prev := active + ".prev"
	if err := os.Remove(prev); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("update: remove stale prev: %w", err)
	}
	if err := os.Rename(active, prev); err != nil {
		return fmt.Errorf("update: save previous binary: %w", err)
	}
	if err := os.Rename(newPath, active); err != nil {
		if rerr := os.Rename(prev, active); rerr != nil {
			return errors.Join(fmt.Errorf("update: install binary: %w", err), fmt.Errorf("update: restore previous binary: %w", rerr))
		}
		return fmt.Errorf("update: install binary: %w", err)
	}
	return nil
}

// Rollback restores the previous binary recorded by pending.json. With data it
// also restores the paired pre-migration DB; a missing/mismatched pair is
// ErrNoPending (the caller maps it to exit 2, never guessing).
func Rollback(dataDir string, withData bool) (err error) {
	unlock, err := acquireLock(filepath.Join(dataDir, "update.lock"))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	p, err := readPending(dataDir)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(p.PrevBinary, ".prev") {
		return ErrNoPending
	}
	active := strings.TrimSuffix(p.PrevBinary, ".prev")
	if active == "" {
		return ErrNoPending
	}
	if withData {
		if p.PremigrateDB == "" {
			return ErrNoPending
		}
		if _, err := os.Stat(p.PremigrateDB); err != nil {
			return ErrNoPending
		}
		if err := restoreDB(dataDir, p.PremigrateDB); err != nil {
			return err
		}
	}
	if err := restoreBinary(active, p.PrevBinary); err != nil {
		return err
	}
	return removePending(dataDir)
}

func restoreBinary(active, prev string) error {
	tmp := active + ".rollback"
	if err := os.Rename(active, tmp); err != nil {
		return fmt.Errorf("update: save current binary: %w", err)
	}
	if err := os.Rename(prev, active); err != nil {
		if rerr := os.Rename(tmp, active); rerr != nil {
			return errors.Join(fmt.Errorf("update: restore previous binary: %w", err), fmt.Errorf("update: restore current binary: %w", rerr))
		}
		return fmt.Errorf("update: restore previous binary: %w", err)
	}
	return removeIfExists(tmp)
}

func restoreDB(dataDir, src string) (err error) {
	srcRoot, err := os.OpenRoot(filepath.Dir(src))
	if err != nil {
		return fmt.Errorf("update: open backup dir: %w", err)
	}
	defer func() { err = errors.Join(err, closeRoot(srcRoot)) }()
	in, err := srcRoot.Open(filepath.Base(src))
	if err != nil {
		return fmt.Errorf("update: open premigrate backup: %w", err)
	}
	defer func() { err = errors.Join(err, closeFile(in)) }()
	dstRoot, err := os.OpenRoot(dataDir)
	if err != nil {
		return fmt.Errorf("update: open data dir: %w", err)
	}
	defer func() { err = errors.Join(err, closeRoot(dstRoot)) }()
	out, err := dstRoot.OpenFile("damnbot.db.restore", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("update: create restore temp: %w", err)
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return errors.Join(fmt.Errorf("update: copy backup: %w", err), removeRootFile(dstRoot, "damnbot.db.restore"))
	}
	if err := dstRoot.Remove("damnbot.db"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("update: replace database: %w", err)
	}
	if err := dstRoot.Rename("damnbot.db.restore", "damnbot.db"); err != nil {
		return fmt.Errorf("update: publish database: %w", err)
	}
	return errors.Join(removeRootFile(dstRoot, "damnbot.db-wal"), removeRootFile(dstRoot, "damnbot.db-shm"))
}

func acquireLock(path string) (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("update: create data dir: %w", err)
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("update: open data dir: %w", err)
	}
	f, err := root.OpenFile(filepath.Base(path), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("update: open lock: %w", err), closeRoot(root))
	}
	closeRootErr := func() error { return closeRoot(root) }
	unlock, err := lockFile(f)
	if err != nil {
		return nil, errors.Join(err, closeFile(f), closeRootErr())
	}
	return func() error {
		return errors.Join(unlock(), closeFile(f), closeRootErr())
	}, nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("update: remove %s: %w", filepath.Base(path), err)
	}
	return nil
}

func removeRootFile(root *os.Root, name string) error {
	if err := root.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("update: remove %s: %w", name, err)
	}
	return nil
}

func closeRoot(root *os.Root) error {
	if err := root.Close(); err != nil {
		return fmt.Errorf("update: close root: %w", err)
	}
	return nil
}

func closeFile(f *os.File) error {
	if err := f.Close(); err != nil {
		return fmt.Errorf("update: close file: %w", err)
	}
	return nil
}

func pendingPath(dataDir string) string { return filepath.Join(dataDir, updateDir, pendingFile) }

func writePending(dataDir string, p *pending) error {
	dir := filepath.Join(dataDir, updateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("update: create update dir: %w", err)
	}
	data, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("update: encode pending: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, pendingFile), data, 0o600); err != nil {
		return fmt.Errorf("update: write pending: %w", err)
	}
	return nil
}

func readPending(dataDir string) (p pending, err error) {
	root, err := os.OpenRoot(filepath.Join(dataDir, updateDir))
	if err != nil {
		return pending{}, ErrNoPending
	}
	defer func() { err = errors.Join(err, closeRoot(root)) }()
	data, err := root.ReadFile(pendingFile)
	if err != nil {
		return pending{}, ErrNoPending
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return pending{}, ErrNoPending
	}
	if p.PrevBinary == "" || p.TargetVersion == "" {
		return pending{}, ErrNoPending
	}
	return p, nil
}

func removePending(dataDir string) error {
	err := os.Remove(pendingPath(dataDir))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("update: remove pending: %w", err)
	}
	return nil
}

func findPremigrate(dataDir string) string {
	dir := filepath.Join(dataDir, "backups")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	best := ""
	var newest time.Time
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "premigrate-") || !strings.HasSuffix(name, ".db") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			best, newest = filepath.Join(dir, name), info.ModTime()
		}
	}
	return best
}
