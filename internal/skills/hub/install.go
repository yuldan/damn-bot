package hub

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/damnbot/damnbot/internal/skills"
)

// Approve promotes one quarantined skill. caller must be the configured
// Owner: an empty owner or a mismatch is refused before the store is touched,
// so neither an agent nor an unset environment can approve a hub skill.
func (h *Hub) Approve(ctx context.Context, caller string, memoryID int64) error {
	if h.owner == "" || caller == "" || subtle.ConstantTimeCompare([]byte(caller), []byte(h.owner)) != 1 {
		return fmt.Errorf("%w: caller %q", ErrNotOwner, caller)
	}
	if memoryID <= 0 {
		return fmt.Errorf("%w: memory id %d", ErrIndex, memoryID)
	}
	if err := h.svc.Approve(ctx, memoryID); err != nil {
		return fmt.Errorf("skills hub: approve: %w", err)
	}
	return nil
}

// Install writes an approved skill's verified SKILL.md into
// <targetDir>/<name>/SKILL.md atomically: the directory appears with a rename
// only after the file is complete. A skill that is not approved, an index
// whose pinned hash does not match the downloaded bytes, and a name outside
// the closed skill shape all write nothing.
func (h *Hub) Install(ctx context.Context, src Source, name, targetDir string) (string, error) {
	return h.InstallAccepted(ctx, src, name, targetDir, "")
}

// InstallAccepted is [Hub.Install] with the typed acknowledgement of a
// high-risk ToS entry (GAP-GW-07): the user must retype the exact entry name.
func (h *Hub) InstallAccepted(ctx context.Context, src Source, name, targetDir, typed string) (string, error) {
	if !entryNameRe.MatchString(name) {
		return "", fmt.Errorf("%w: name %q", ErrIndex, name)
	}
	if targetDir == "" {
		return "", fmt.Errorf("%w: empty target dir", ErrIndex)
	}
	if err := h.requireApproved(ctx, name); err != nil {
		return "", err
	}
	idx, err := h.Index(ctx, src)
	if err != nil {
		return "", err
	}
	e, err := idx.Find(name)
	if err != nil {
		return "", err
	}
	if terr := skills.RequireToSAck(e.ToS, e.Name, typed); terr != nil {
		return "", fmt.Errorf("skills hub: %s: %w", e.Name, terr)
	}
	raw, err := h.fetchEntry(ctx, src, &e)
	if err != nil {
		return "", err
	}
	if _, err := parseRaw(src, &e, raw); err != nil {
		return "", err
	}
	if werr := writeAtomic(targetDir, name, raw); werr != nil {
		return "", werr
	}
	return filepath.Join(targetDir, name, "SKILL.md"), nil
}

// requireApproved reads the approved catalog and insists on an exact name.
// The check happens before the network and before any write.
func (h *Hub) requireApproved(ctx context.Context, name string) error {
	list, err := h.svc.Retrieve(ctx, name, skills.MaxOffer)
	if err != nil {
		return fmt.Errorf("skills hub: retrieve: %w", err)
	}
	for i := range list {
		if list[i].Name == name {
			return nil
		}
	}
	return fmt.Errorf("%w: %q", ErrNotApproved, name)
}

// writeAtomic stages the file in a dot-prefixed directory and renames that
// directory to the skill name. os.Root confines every operation to targetDir,
// so neither name nor a symlinked target can escape it.
func writeAtomic(targetDir, name string, raw []byte) (outErr error) {
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		return fmt.Errorf("skills hub: target dir: %w", err)
	}
	root, err := os.OpenRoot(targetDir)
	if err != nil {
		return fmt.Errorf("skills hub: target root: %w", err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil && outErr == nil {
			outErr = fmt.Errorf("skills hub: close target: %w", cerr)
		}
	}()
	tmp := ".install-" + name
	if err := root.RemoveAll(tmp); err != nil {
		return fmt.Errorf("skills hub: stale temp: %w", err)
	}
	if err := root.Mkdir(tmp, 0o700); err != nil {
		return fmt.Errorf("skills hub: temp dir: %w", err)
	}
	defer func() { _ = root.RemoveAll(tmp) }() //nolint:errcheck // best-effort cleanup after a successful rename
	if err := root.WriteFile(filepath.Join(tmp, "SKILL.md"), raw, 0o600); err != nil {
		return fmt.Errorf("skills hub: write: %w", err)
	}
	if _, err := root.Lstat(name); err == nil {
		return fmt.Errorf("%w: %q", ErrInstalled, name)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("skills hub: target check: %w", err)
	}
	if err := root.Rename(tmp, name); err != nil {
		return fmt.Errorf("skills hub: rename: %w", err)
	}
	return nil
}

// Uninstall removes one installed skill directory. The name must be a closed
// skill name (never a path), and a missing skill is reported rather than
// silently ignored.
func Uninstall(targetDir, name string) error {
	if !entryNameRe.MatchString(name) {
		return fmt.Errorf("%w: name %q", ErrIndex, name)
	}
	root, err := os.OpenRoot(targetDir)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %q", ErrNotInstalled, name)
	}
	if err != nil {
		return fmt.Errorf("skills hub: target root: %w", err)
	}
	defer func() { _ = root.Close() }() //nolint:errcheck // removal already committed
	if _, err := root.Lstat(name); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: %q", ErrNotInstalled, name)
	} else if err != nil {
		return fmt.Errorf("skills hub: stat: %w", err)
	}
	if err := root.RemoveAll(name); err != nil {
		return fmt.Errorf("skills hub: uninstall: %w", err)
	}
	return nil
}

// SyncResult reports one sync run. Installed entries are in quarantine
// (pending review); rejected entries had a high threat finding; failed entries
// never reached the store.
type SyncResult struct {
	Installed []string
	Rejected  []string
	Failed    []string
}

// Sync downloads every entry of one source through the quarantine path. It
// never promotes a skill: the result is pending until [Hub.Approve].
func (h *Hub) Sync(ctx context.Context, src Source) (SyncResult, error) {
	return h.sync(ctx, src, func(Entry) bool { return true })
}

// SyncOptional syncs only entries under optional-skills/ (SKILL-017). The
// filter is applied to the source index, so nothing is installed implicitly
// when the caller does not ask for the optional corpus.
func (h *Hub) SyncOptional(ctx context.Context, src Source) (SyncResult, error) {
	return h.sync(ctx, src, func(e Entry) bool { return strings.HasPrefix(e.Path, "optional-skills/") })
}

func (h *Hub) sync(ctx context.Context, src Source, keep func(Entry) bool) (SyncResult, error) {
	var res SyncResult
	idx, err := h.Index(ctx, src)
	if err != nil {
		return res, err
	}
	var failed []error
	n := 0
	for i := range idx.Entries {
		e := &idx.Entries[i]
		if !keep(*e) {
			continue
		}
		n++
		inst, derr := h.Download(ctx, src, e)
		switch {
		case derr != nil:
			res.Failed = append(res.Failed, e.Name)
			failed = append(failed, fmt.Errorf("%s: %w", e.Name, derr))
		case inst.Status == skills.StatusRejected:
			res.Rejected = append(res.Rejected, e.Name)
		default:
			res.Installed = append(res.Installed, e.Name)
		}
	}
	if n == 0 {
		return res, fmt.Errorf("%w: no matching entry", ErrNoEntry)
	}
	if len(failed) > 0 {
		return res, fmt.Errorf("skills hub: sync: %w", errors.Join(failed...))
	}
	return res, nil
}
