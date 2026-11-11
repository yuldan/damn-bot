package setup

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Options are the explicit inputs of `damnbot setup --uninstall`.
type Options struct {
	ManifestPath string
	DryRun       bool
	PurgeData    bool
	DataDir      string
	ConfigDir    string
	HomeDir      string
	Out          io.Writer
}

// Report is the per-path outcome.
type Report struct {
	Removed   []string
	Kept      []string
	DataDir   string
	ConfigDir string
	Purged    bool
}

// Uninstall removes only paths recorded in the installer manifest.
func Uninstall(o *Options) (Report, error) {
	var rep Report
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	o.Out = out
	rep.DataDir, rep.ConfigDir = o.DataDir, o.ConfigDir
	m, err := ReadManifest(o.ManifestPath)
	if err != nil {
		return rep, err
	}
	for _, b := range m.Blocks {
		if err := uninstallBlock(o.DryRun, out, b, &rep); err != nil {
			return rep, err
		}
	}
	for _, f := range m.Files {
		if f.Kind == "rc" {
			continue
		}
		if err := uninstallFile(o.DryRun, out, f, &rep); err != nil {
			return rep, err
		}
	}
	if o.DryRun {
		emitf(out, "dry-run: no changes written; data dir %s, config dir %s\n", o.DataDir, o.ConfigDir)
		return rep, nil
	}
	removeDirs(m.Dirs, &rep)
	if rerr := os.Remove(o.ManifestPath); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		return rep, fmt.Errorf("remove manifest: %w", rerr)
	}
	rep.Removed = append(rep.Removed, o.ManifestPath)
	if o.PurgeData {
		if err := purgeDirs(o, &rep); err != nil {
			return rep, err
		}
	}
	state := "kept; use --purge-data to remove"
	if o.PurgeData {
		state = "purged"
	}
	emitf(out, "data dir: %s (%s)\n", o.DataDir, state)
	emitf(out, "config dir: %s (%s)\n", o.ConfigDir, state)
	return rep, nil
}

func removeDirs(dirs []string, rep *Report) {
	for _, d := range dirs {
		if err := os.Remove(d); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				rep.Kept = append(rep.Kept, fmt.Sprintf("%s (directory not empty)", d))
			}
		} else {
			rep.Removed = append(rep.Removed, d)
		}
	}
}

func purgeDirs(o *Options, rep *Report) error {
	for _, dir := range uniqueDirs(o.DataDir, o.ConfigDir) {
		if err := purge(dir, o.HomeDir, o.Out); err != nil {
			return err
		}
		if dir != "" {
			rep.Purged = true
		}
	}
	return nil
}

func emit(w io.Writer, s string) {
	if _, err := io.WriteString(w, s); err != nil {
		return
	}
}
func emitf(w io.Writer, format string, args ...any) { emit(w, fmt.Sprintf(format, args...)) }
func uninstallFile(dry bool, out io.Writer, f File, rep *Report) error {
	info, err := os.Lstat(f.Path)
	if errors.Is(err, fs.ErrNotExist) {
		rep.Kept = append(rep.Kept, f.Path+" (missing)")
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", f.Path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		rep.Kept = append(rep.Kept, f.Path+" (not a regular file)")
		return nil
	}
	raw, err := os.ReadFile(f.Path) // #nosec G304 -- validated manifest path
	if err != nil {
		return fmt.Errorf("read %s: %w", f.Path, err)
	}
	if sum := sha256.Sum256(raw); hex.EncodeToString(sum[:]) != f.SHA256After {
		rep.Kept = append(rep.Kept, f.Path+" (content changed)")
		emitf(out, "kept %s (content changed)\n", f.Path)
		return nil
	}
	if dry {
		emitf(out, "would remove %s\n", f.Path)
		rep.Removed = append(rep.Removed, f.Path)
		return nil
	}
	if rerr := os.Remove(f.Path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", f.Path, rerr)
	}
	emitf(out, "removed %s\n", f.Path)
	rep.Removed = append(rep.Removed, f.Path)
	return nil
}

func uninstallBlock(dry bool, out io.Writer, b Block, rep *Report) error {
	info, err := os.Lstat(b.Path)
	if errors.Is(err, fs.ErrNotExist) {
		rep.Kept = append(rep.Kept, b.Path+" (missing rc)")
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat %s: %w", b.Path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		rep.Kept = append(rep.Kept, b.Path+" (not a regular file)")
		return nil
	}
	raw, err := os.ReadFile(b.Path) // #nosec G304 -- validated manifest path
	if err != nil {
		return fmt.Errorf("read %s: %w", b.Path, err)
	}
	next, found, err := removeBlock(raw, b)
	if err != nil {
		return fmt.Errorf("%s: %w", b.Path, err)
	}
	if !found {
		rep.Kept = append(rep.Kept, b.Path+" (no marker block)")
		return nil
	}
	if dry {
		emit(out, unifiedDiff(b.Path, raw, next))
		rep.Removed = append(rep.Removed, b.Path+" (block)")
		return nil
	}
	if werr := writePreserve(b.Path, next, info.Mode().Perm()); werr != nil {
		return fmt.Errorf("write %s: %w", b.Path, werr)
	}
	emitf(out, "removed block %s\n", b.Path)
	rep.Removed = append(rep.Removed, b.Path+" (block)")
	return nil
}

func removeBlock(data []byte, b Block) ([]byte, bool, error) {
	start, afterBegin, ok := findLine(data, b.Begin, 0)
	if !ok {
		if _, _, trailing := findLine(data, b.End, 0); trailing {
			return nil, false, errors.New("end marker without begin marker")
		}
		return data, false, nil
	}
	if _, _, extra := findLine(data, b.Begin, afterBegin); extra {
		return nil, false, errors.New("duplicate begin marker")
	}
	_, afterEnd, ok := findLine(data, b.End, afterBegin)
	if !ok {
		return nil, false, errors.New("begin marker without end marker")
	}
	if _, _, extra := findLine(data, b.End, afterEnd); extra {
		return nil, false, errors.New("duplicate end marker")
	}
	cut := start
	if b.AddedNewline && start > 0 && data[start-1] == '\n' {
		cut--
	}
	next := make([]byte, 0, len(data)-(afterEnd-cut))
	next = append(next, data[:cut]...)
	next = append(next, data[afterEnd:]...)
	return next, true, nil
}

func findLine(data []byte, marker string, from int) (start, next int, ok bool) {
	for i := from; i < len(data); {
		end := len(data)
		if j := bytes.IndexByte(data[i:], '\n'); j >= 0 {
			end = i + j + 1
		}
		line := bytes.TrimSuffix(data[i:end], []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))
		if string(line) == marker {
			return i, end, true
		}
		i = end
	}
	return 0, 0, false
}

func writePreserve(path string, data []byte, mode os.FileMode) (err error) {
	f, err := os.CreateTemp(filepath.Dir(path), ".damnbot-uninstall-*")
	if err != nil {
		return fmt.Errorf("uninstall temp: %w", err)
	}
	name := f.Name()
	defer func() {
		if cerr := f.Close(); cerr != nil && !errors.Is(cerr, os.ErrClosed) && err == nil {
			err = fmt.Errorf("uninstall close: %w", cerr)
		}
		if rerr := os.Remove(name); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) && err == nil {
			err = fmt.Errorf("uninstall cleanup: %w", rerr)
		}
	}()
	if merr := f.Chmod(mode); merr != nil {
		return fmt.Errorf("uninstall mode: %w", merr)
	}
	if _, werr := f.Write(data); werr != nil {
		return fmt.Errorf("uninstall write: %w", werr)
	}
	if cerr := f.Close(); cerr != nil {
		return fmt.Errorf("uninstall close: %w", cerr)
	}
	if rerr := os.Rename(name, path); rerr != nil {
		return fmt.Errorf("uninstall publish: %w", rerr)
	}
	return nil
}

func purge(dir, home string, out io.Writer) error {
	if dir == "" {
		return nil
	}
	clean := filepath.Clean(dir)
	if !filepath.IsAbs(clean) || filepath.Dir(clean) == clean {
		return fmt.Errorf("refusing to purge unsafe dir %q", dir)
	}
	if home != "" && clean == filepath.Clean(home) {
		return fmt.Errorf("refusing to purge HOME %q", dir)
	}
	if home != "" && strings.HasPrefix(filepath.Clean(home)+string(filepath.Separator), clean+string(filepath.Separator)) {
		return fmt.Errorf("refusing to purge an ancestor of HOME %q", dir)
	}
	if err := os.RemoveAll(clean); err != nil {
		return fmt.Errorf("purge %s: %w", clean, err)
	}
	emitf(out, "purged %s\n", clean)
	return nil
}

func uniqueDirs(dirs ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range dirs {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	return out
}

func unifiedDiff(path string, old, next []byte) string {
	a := strings.Split(string(old), "\n")
	b := strings.Split(string(next), "\n")
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	if prefix == len(a) && prefix == len(b) {
		return ""
	}
	const context = 3
	start := max(0, prefix-context)
	aEnd := min(len(a), len(a)-suffix+context)
	bEnd := min(len(b), len(b)-suffix+context)
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n@@ -%d,%d +%d,%d @@\n", path, path, start+1, aEnd-start, start+1, bEnd-start)
	for i := start; i < prefix; i++ {
		fmt.Fprintf(&sb, " %s\n", a[i])
	}
	for i := prefix; i < len(a)-suffix; i++ {
		fmt.Fprintf(&sb, "-%s\n", a[i])
	}
	for i := prefix; i < len(b)-suffix; i++ {
		fmt.Fprintf(&sb, "+%s\n", b[i])
	}
	for i := len(a) - suffix; i < aEnd; i++ {
		fmt.Fprintf(&sb, " %s\n", a[i])
	}
	return sb.String()
}
