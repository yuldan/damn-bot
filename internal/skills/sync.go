package skills

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
)

// SyncResult is the outcome of one SyncBundled. Names are skill names, not
// paths, and every slice is sorted by the corpus walk order.
type SyncResult struct {
	Installed []string
	Unchanged []string
	Conflicts []string
	Failed    []string
}

// nameLookup is implemented by stores that can find one approved skill by its
// exact name. The Store interface stays at five methods (E-09); stores without
// this method fall back to Search.
type nameLookup interface {
	ByName(ctx context.Context, name string) (Skill, bool, error)
}

// SyncBundled copies every embedded bundled/<name>/SKILL.md into targetDir and
// indexes it as a trusted builtin through Service.Install. It never writes the
// store directly and never overwrites a skill whose bytes or stored hash
// differ from the embedded copy: that is a conflict and is reported, not
// replaced. A skill that fails to parse, lint, or install is not left in the
// catalog or on disk.
func (s *Service) SyncBundled(ctx context.Context, fsys fs.FS, targetDir string) (res SyncResult, outErr error) {
	if targetDir == "" {
		return res, fmt.Errorf("%w: empty bundled target", ErrInvalid)
	}
	paths, err := fs.Glob(fsys, "bundled/*/SKILL.md")
	if err != nil {
		return res, fmt.Errorf("bundled corpus: %w", err)
	}
	if len(paths) == 0 {
		return res, fmt.Errorf("%w: bundled corpus is empty", ErrInvalid)
	}
	if merr := os.MkdirAll(targetDir, 0o700); merr != nil {
		return res, fmt.Errorf("bundled target: %w", merr)
	}
	root, err := os.OpenRoot(targetDir)
	if err != nil {
		return res, fmt.Errorf("bundled target: %w", err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			outErr = errors.Join(outErr, fmt.Errorf("bundled target close: %w", cerr))
		}
	}()

	var failed []error
	for _, src := range paths {
		name := path.Base(path.Dir(src))
		raw, rerr := fs.ReadFile(fsys, src)
		if rerr != nil {
			failed = append(failed, fmt.Errorf("%s: %w", name, rerr))
			res.Failed = append(res.Failed, name)
			continue
		}
		sk, perr := Parse(raw, filepath.Join(targetDir, name, "SKILL.md"), SourceBuiltin)
		if perr != nil {
			failed = append(failed, fmt.Errorf("%s: %w", name, perr))
			res.Failed = append(res.Failed, name)
			continue
		}
		if sk.Name != name {
			failed = append(failed, fmt.Errorf("%s: front matter name %q does not match directory", name, sk.Name))
			res.Failed = append(res.Failed, name)
			continue
		}
		switch outcome, serr := s.syncOne(ctx, root, name, raw, &sk); {
		case serr != nil:
			failed = append(failed, serr)
			res.Failed = append(res.Failed, name)
		case outcome == syncConflict:
			res.Conflicts = append(res.Conflicts, name)
		case outcome == syncUnchanged:
			res.Unchanged = append(res.Unchanged, name)
		default:
			res.Installed = append(res.Installed, name)
		}
	}
	if len(failed) > 0 {
		return res, fmt.Errorf("bundled sync: %w", errors.Join(failed...))
	}
	return res, nil
}

type syncOutcome int

const (
	syncInstalled syncOutcome = iota
	syncUnchanged
	syncConflict
)

// syncOne reconciles one embedded skill with the target file and the store.
// syncConflict means the existing bytes or stored hash differ from the
// embedded copy; nothing was written.
func (s *Service) syncOne(ctx context.Context, root *os.Root, name string, raw []byte, sk *Skill) (syncOutcome, error) {
	state, found, conflict, err := s.reconcile(ctx, root, name, raw, sk)
	if err != nil || conflict {
		return syncConflict, err
	}
	if found {
		return syncUnchanged, nil
	}
	if _, ierr := s.Install(ctx, sk); ierr != nil {
		if state == bundledMissing {
			if rerr := removeBundledFile(root, name); rerr != nil {
				ierr = errors.Join(ierr, rerr)
			}
		}
		return syncConflict, fmt.Errorf("%s: %w", name, ierr)
	}
	return syncInstalled, nil
}

// reconcile checks the target directory, the stored skill, and the file, then
// writes the file when it is absent. It reports conflict without writing when
// any existing copy differs from the embedded bytes.
func (s *Service) reconcile(ctx context.Context, root *os.Root, name string, raw []byte, sk *Skill) (state bundledFileState, found, conflict bool, outErr error) {
	dirExists, realDir, err := bundledDir(root, name)
	if err != nil {
		return state, false, false, err
	}
	if dirExists && !realDir {
		warnBundled(ctx, name, "target is not a directory")
		return state, false, true, nil
	}
	stored, found, err := s.lookup(ctx, sk.Name)
	if err != nil {
		return state, false, false, err
	}
	if found && stored.SHA256 != sk.SHA256 {
		warnBundled(ctx, name, "stored skill differs")
		return state, true, true, nil
	}
	state, err = bundledFile(root, filepath.Join(name, "SKILL.md"), raw)
	if err != nil {
		return state, found, false, err
	}
	if state == bundledDiffers {
		warnBundled(ctx, name, "file differs")
		return state, found, true, nil
	}
	if state != bundledMissing {
		return state, found, false, nil
	}
	if dirExists {
		empty, eerr := bundledDirEmpty(root, name)
		if eerr != nil {
			return state, found, false, eerr
		}
		if !empty {
			warnBundled(ctx, name, "directory is not empty")
			return state, found, true, nil
		}
	}
	if werr := writeBundledFile(root, name, raw); werr != nil {
		return state, found, false, werr
	}
	return state, found, false, nil
}

// warnBundled reports a skipped bundled skill without failing the run.
func warnBundled(ctx context.Context, name, reason string) {
	slog.WarnContext(ctx, "skills: bundled skill not overwritten",
		slog.String("name", name), slog.String("reason", reason))
}

// lookup finds an existing skill by exact name. Stores that do not implement
// nameLookup fall back to Search; a production store implements ByName.
func (s *Service) lookup(ctx context.Context, name string) (Skill, bool, error) {
	if l, ok := s.store.(nameLookup); ok {
		sk, found, lerr := l.ByName(ctx, name)
		if lerr != nil {
			return Skill{}, false, fmt.Errorf("bundled lookup %s: %w", name, lerr)
		}
		return sk, found, nil
	}
	list, err := s.store.Search(ctx, name, 20)
	if err != nil {
		return Skill{}, false, fmt.Errorf("bundled lookup %s: %w", name, err)
	}
	for i := range list {
		if list[i].Name == name {
			return list[i], true, nil
		}
	}
	return Skill{}, false, nil
}

// bundledDir reports whether targetDir/<name> exists and is a real directory
// (not a symlink or a regular file).
func bundledDir(root *os.Root, name string) (exists, real bool, err error) {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, true, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("bundled dir %s: %w", name, err)
	}
	return true, info.IsDir() && info.Mode()&os.ModeSymlink == 0, nil
}

// bundledDirEmpty reports whether an existing skill directory has no entries.
func bundledDirEmpty(root *os.Root, name string) (empty bool, outErr error) {
	f, err := root.Open(name)
	if err != nil {
		return false, fmt.Errorf("bundled dir %s: %w", name, err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			outErr = errors.Join(outErr, fmt.Errorf("bundled dir %s close: %w", name, cerr))
		}
	}()
	entries, rerr := f.ReadDir(1)
	if errors.Is(rerr, io.EOF) {
		return true, nil
	}
	if rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		return false, fmt.Errorf("bundled dir %s: %w", name, rerr)
	}
	return len(entries) == 0, nil
}

// bundledFileState classifies the target file against the embedded bytes.
type bundledFileState int

const (
	bundledMissing bundledFileState = iota
	bundledSame
	bundledDiffers
)

func bundledFile(root *os.Root, rel string, want []byte) (bundledFileState, error) {
	info, err := root.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		return bundledMissing, nil
	}
	if err != nil {
		return bundledDiffers, fmt.Errorf("bundled file %s: %w", rel, err)
	}
	if !info.Mode().IsRegular() {
		return bundledDiffers, nil
	}
	cur, err := root.ReadFile(rel)
	if err != nil {
		return bundledDiffers, fmt.Errorf("bundled file %s: %w", rel, err)
	}
	if bytes.Equal(cur, want) {
		return bundledSame, nil
	}
	return bundledDiffers, nil
}

// writeBundledFile creates targetDir/<name>/SKILL.md atomically. It is called
// only when the file is absent and the directory is absent or empty.
func writeBundledFile(root *os.Root, name string, raw []byte) error {
	dir := name
	if err := root.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("bundled dir %s: %w", dir, err)
	}
	dest := filepath.Join(dir, "SKILL.md")
	tmp := filepath.Join(dir, fmt.Sprintf(".SKILL.md-%d", os.Getpid()))
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("bundled temp %s: %w", tmp, err)
	}
	if _, werr := f.Write(raw); werr != nil {
		return fmt.Errorf("bundled write %s: %w", tmp, errors.Join(werr, f.Close(), root.Remove(tmp)))
	}
	if cerr := f.Close(); cerr != nil {
		return fmt.Errorf("bundled close %s: %w", tmp, errors.Join(cerr, root.Remove(tmp)))
	}
	if rerr := root.Rename(tmp, dest); rerr != nil {
		return fmt.Errorf("bundled rename %s: %w", dest, errors.Join(rerr, root.Remove(tmp)))
	}
	return nil
}

// removeBundledFile removes a file written by this call that failed to
// install, then removes the now-empty directory. A missing path is not an
// error: the goal is to leave no partial skill behind.
func removeBundledFile(root *os.Root, name string) error {
	var errs []error
	for _, rel := range []string{filepath.Join(name, "SKILL.md"), name} {
		if err := root.Remove(rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("bundled cleanup %s: %w", rel, err))
		}
	}
	return errors.Join(errs...)
}
