package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func Repair(ctx context.Context, o *InstallOptions) (status string, err error) {
	if o.DataDir == "" || o.BinaryPath == "" {
		return "", errors.New("update: repair needs data dir and binary path")
	}
	unlock, err := acquireLock(filepath.Join(o.DataDir, "update.lock"))
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	p, err := readPending(o.DataDir)
	if err != nil {
		return "", err
	}
	if p.PrevBinary != o.BinaryPath+".prev" {
		return "", ErrNoPending
	}
	fail := func(cause string, causeErr error) (string, error) {
		if rerr := rollbackPrevious(o.DataDir, &p); rerr != nil {
			return "", errors.Join(causeErr, rerr)
		}
		return "rolled back: " + cause, nil
	}
	if verr := p.verify(ctx, o); verr != nil {
		return fail(verr.Error(), verr)
	}
	got, herr := fileSHA256(o.BinaryPath)
	if herr != nil || got != p.TargetSHA256 {
		return fail("installed binary failed checksum", herr)
	}
	health := o.HealthCheck
	if health == nil {
		health = quickHealth
	}
	if herr = health(ctx, o.BinaryPath); herr != nil {
		return fail("health check failed", herr)
	}
	if err := removePending(o.DataDir); err != nil {
		return "", err
	}
	cleanupStaged(o.BinaryPath)
	return "verified " + p.TargetVersion, nil
}

func (p *pending) verify(ctx context.Context, o *InstallOptions) error {
	if p.TargetSHA256 == "" {
		return errors.New("pending record has no target checksum")
	}
	r, err := resolve(ctx, o.Client, o.FeedURL, o.Channel, o.PublicKey)
	if err != nil {
		return err
	}
	if r.man.Version != p.TargetVersion || r.asset.SHA256 != p.TargetSHA256 {
		return errors.New("pending target does not match the signed manifest")
	}
	return nil
}

func rollbackPrevious(dataDir string, p *pending) error {
	active := strings.TrimSuffix(p.PrevBinary, ".prev")
	if err := rollback(dataDir, false); err != nil {
		return err
	}
	cleanupStaged(active)
	return nil
}

func cleanupStaged(active string) {
	_ = removeIfExists(filepath.Join(filepath.Dir(active), "damnbot.new"+filepath.Ext(active)))
}

func fileSHA256(path string) (sha string, err error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return "", fmt.Errorf("update: repair: open binary dir: %w", err)
	}
	defer func() { err = errors.Join(err, closeRoot(root)) }()
	f, err := root.Open(filepath.Base(path))
	if err != nil {
		return "", fmt.Errorf("update: repair: open binary: %w", err)
	}
	defer func() { err = errors.Join(err, closeFile(f)) }()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxBinarySize+1)); err != nil {
		return "", fmt.Errorf("update: repair: hash binary: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
