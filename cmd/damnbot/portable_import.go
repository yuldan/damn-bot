//go:build !minimal

// Import side of the portable archive (GAP-OPS-05): per-item conflict
// resolution (keep-local/take-remote/both) and the threat scan + quarantine
// every imported skill and memory passes through (P10).
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/memstore"
	"github.com/damnbot/damnbot/internal/memory"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/secret"
	"github.com/damnbot/damnbot/internal/skills"
)

type conflictMode string

const (
	keepLocal  conflictMode = "keep-local"
	takeRemote conflictMode = "take-remote"
	bothItems  conflictMode = "both"
)

func parseConflictMode(s string) (conflictMode, error) {
	switch m := conflictMode(s); m {
	case keepLocal, takeRemote, bothItems:
		return m, nil
	default:
		return "", fmt.Errorf("unknown conflict mode %q (want keep-local, take-remote, or both)", cleanArg(s))
	}
}

// normalizePortableText makes Windows (CRLF) and Unix (LF) archives compare
// and install identically, so a re-import is idempotent on every OS.
func normalizePortableText(data []byte) []byte {
	return bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
}

func importPortable(ctx context.Context, p config.Paths, path string, mode conflictMode, dry bool, key secret.BackupKey, codec ctxdb.Codec) (portableCounts, error) { //nolint:gocognit,gocyclo,funlen // one switch over the three closed item kinds
	var c portableCounts
	m, blobs, err := openPortableArchive(path, key)
	if err != nil {
		return c, err
	}
	red := portableRedactor()
	dbPaths := p
	cleanup := func() {}
	if dry {
		if _, serr := os.Stat(filepath.Join(p.DataDir, "damnbot.db")); errors.Is(serr, fs.ErrNotExist) {
			tmp, terr := os.MkdirTemp("", "damnbot-archive-dry-")
			if terr != nil {
				return c, fmt.Errorf("dry-run state: %w", terr)
			}
			dbPaths.DataDir, codec = tmp, nil
			cleanup = func() { _ = os.RemoveAll(tmp) } //nolint:errcheck // temporary dry-run state
		}
	}
	defer cleanup()
	db, err := openPortableDB(ctx, dbPaths, codec, true)
	if err != nil {
		return c, err
	}
	defer closePortableDB(ctx, db)
	if !dry {
		if derr := os.MkdirAll(p.ConfigDir, 0o700); derr != nil {
			return c, fmt.Errorf("create config dir: %w", derr)
		}
	}
	svc, err := newSkillService(ctx, db, red)
	if err != nil {
		return c, err
	}
	mem, err := openMemory(db, red)
	if err != nil {
		return c, err
	}
	for i := range m.Items {
		it := &m.Items[i]
		data := blobs[i]
		switch it.Kind {
		case portableConfig:
			changed, conflict, cerr := importPortableConfig(p, data, mode, dry, red)
			c.add(portableConfig, changed)
			if changed {
				c.ChangedItems = append(c.ChangedItems, "config")
			}
			if conflict {
				c.Conflicts++
			}
			if cerr != nil {
				return c, cerr
			}
		case portableSkill:
			changed, conflict, protected, rejected, serr := importPortableSkill(ctx, db, svc, p.ConfigDir, it, data, mode, dry)
			c.add(portableSkill, changed)
			if changed {
				c.ChangedItems = append(c.ChangedItems, "skill:"+it.Name)
			}
			if conflict {
				c.Conflicts++
			}
			if protected {
				c.Protected++
			}
			if rejected {
				c.Rejected++
			}
			if serr != nil {
				return c, serr
			}
		case portableMemory:
			if strings.TrimSpace(it.Category) == "" {
				return c, fmt.Errorf("memory entry has no category")
			}
			changed, rejected, merr := importPortableMemory(ctx, db, mem, it, data, dry)
			c.add(portableMemory, changed)
			if changed {
				c.ChangedItems = append(c.ChangedItems, "memory:"+it.Scope)
			}
			if rejected {
				c.Rejected++
			}
			if merr != nil {
				return c, merr
			}
		}
	}
	return c, nil
}

func importPortableConfig(p config.Paths, data []byte, mode conflictMode, dry bool, red *redact.Redactor) (bool, bool, error) { //nolint:gocyclo // three explicit conflict modes
	clean, err := portableConfigBytes(normalizePortableText(data), red)
	if err != nil {
		return false, false, fmt.Errorf("import config: %w", err)
	}
	local, err := os.ReadFile(p.ConfigFile())
	if errors.Is(err, fs.ErrNotExist) {
		if dry {
			return true, false, nil
		}
		if derr := os.MkdirAll(p.ConfigDir, 0o700); derr != nil {
			return false, false, fmt.Errorf("create config dir: %w", derr)
		}
		return true, false, writeAtomicFile(p.ConfigFile(), ".config.toml.portable-*", clean)
	}
	if err != nil {
		return false, false, fmt.Errorf("read %s: %w", p.ConfigFile(), err)
	}
	local = normalizePortableText(local)
	if bytes.Equal(local, clean) {
		return false, false, nil
	}
	switch mode {
	case keepLocal:
		return false, true, nil
	case takeRemote:
		if dry {
			return true, true, nil
		}
		if derr := os.MkdirAll(p.ConfigDir, 0o700); derr != nil {
			return false, false, fmt.Errorf("create config dir: %w", derr)
		}
		return true, true, writeAtomicFile(p.ConfigFile(), ".config.toml.portable-*", clean)
	case bothItems:
		remote := p.ConfigFile() + ".imported"
		if old, rerr := os.ReadFile(remote); rerr == nil && bytes.Equal(old, clean) { //nolint:gosec // explicit profile config path
			return false, true, nil
		}
		if dry {
			return true, true, nil
		}
		if derr := os.MkdirAll(p.ConfigDir, 0o700); derr != nil {
			return false, false, fmt.Errorf("create config dir: %w", derr)
		}
		return true, true, writeAtomicFile(remote, ".config.toml.imported-*", clean)
	default:
		return false, false, fmt.Errorf("unknown conflict mode %q", mode)
	}
}

type localSkillRow struct {
	id     int64
	sha    string
	source string
	memory int64
}

func localPortableSkills(ctx context.Context, db *ctxdb.Store, name string) ([]localSkillRow, error) {
	var out []localSkillRow
	err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		ids, ierr := tx.Ints(`SELECT id FROM skills WHERE name = ?1 ORDER BY id`, name)
		if ierr != nil {
			return fmt.Errorf("query skill ids: %w", ierr)
		}
		for _, id := range ids {
			var r localSkillRow
			r.id = id
			if gerr := tx.Get(`SELECT sha256, source, COALESCE(memory_id, 0) FROM skills WHERE id = ?1`, []any{id}, &r.sha, &r.source, &r.memory); gerr != nil {
				return fmt.Errorf("query skill %d: %w", id, gerr)
			}
			out = append(out, r)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("local skill %s: %w", name, err)
	}
	return out, nil
}

func importPortableSkill(ctx context.Context, db *ctxdb.Store, svc *skills.Service, configDir string, it *portableItem, data []byte, mode conflictMode, dry bool) (changed, conflict, protected, rejected bool, err error) { //nolint:gocognit // closed conflict matrix
	data = normalizePortableText(data)
	sha := sha256Hex(data)
	local, lerr := localPortableSkills(ctx, db, it.Name)
	if lerr != nil {
		return false, false, false, false, lerr
	}
	for i := range local {
		if local[i].sha == sha {
			return false, false, false, false, nil
		}
	}
	target := it.Name
	if len(local) > 0 { //nolint:nestif // closed conflict matrix
		conflict = true
		switch mode {
		case keepLocal:
			return false, true, false, false, nil
		case takeRemote:
			for i := range local {
				if local[i].memory == 0 {
					protected = true
				}
			}
			if !dry && !protected {
				if derr := deletePortablePendingSkills(ctx, db, it.Name); derr != nil {
					return false, true, false, false, derr
				}
			}
		case bothItems:
			target = suffixedPortableName(it.Name, sha)
		default:
			return false, false, false, false, fmt.Errorf("unknown conflict mode %q", mode)
		}
	}
	if dry {
		return true, conflict, protected, false, nil
	}
	st, serr := installPortableSkill(ctx, svc, configDir, it.Name, target, data)
	if serr != nil {
		return false, conflict, protected, false, serr
	}
	return st != skills.StatusRejected, conflict, protected, st == skills.StatusRejected, nil
}

func deletePortablePendingSkills(ctx context.Context, db *ctxdb.Store, name string) error {
	err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`DELETE FROM skills WHERE name = ?1 AND memory_id IS NOT NULL`, name)
		if xerr != nil {
			return fmt.Errorf("delete pending rows: %w", xerr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("replace pending skill %s: %w", name, err)
	}
	return nil
}

func installPortableSkill(ctx context.Context, svc *skills.Service, configDir, sourceName, targetName string, data []byte) (skills.Status, error) {
	if targetName != sourceName {
		renamed, err := renamePortableSkill(data, targetName)
		if err != nil {
			return "", err
		}
		data = renamed
	}
	stage, err := os.MkdirTemp(configDir, ".portable-skill-")
	if err != nil {
		return "", fmt.Errorf("skill %s: stage: %w", targetName, err)
	}
	defer func() { _ = os.RemoveAll(stage) }() //nolint:errcheck // staged SKILL.md is not trusted state
	path := filepath.Join(stage, "SKILL.md")
	if werr := os.WriteFile(path, data, 0o600); werr != nil {
		return "", fmt.Errorf("skill %s: stage: %w", targetName, werr)
	}
	sk, err := skills.Parse(data, path, skills.SourceImport)
	if err != nil {
		return "", fmt.Errorf("skill %s: %w", targetName, err)
	}
	inst, err := svc.Install(ctx, &sk)
	if err != nil {
		return "", fmt.Errorf("install skill %s: %w", targetName, err)
	}
	return inst.Status, nil
}

func renamePortableSkill(data []byte, name string) ([]byte, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return nil, errors.New("portable skill has no front matter")
	}
	for i := 1; i < len(lines) && strings.TrimSpace(lines[i]) != "---"; i++ {
		k, _, ok := strings.Cut(lines[i], ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), "name") {
			lines[i] = "name: " + name
			return []byte(strings.Join(lines, "\n")), nil
		}
	}
	return nil, errors.New("portable skill has no name in front matter")
}

func suffixedPortableName(name, sha string) string {
	suffix := "-import-" + sha[:8]
	if len(name)+len(suffix) > skills.MaxName {
		name = name[:skills.MaxName-len(suffix)]
	}
	return name + suffix
}

func importPortableMemory(ctx context.Context, db *ctxdb.Store, svc *memory.Service, it *portableItem, data []byte, dry bool) (bool, bool, error) {
	content := string(normalizePortableText(data))
	hits, err := memstore.Search(ctx, db, &memstore.Query{
		Scopes: []string{it.Scope}, Statuses: []string{"pending", "scanned", "approved", "rejected", "retired", "expired"},
		Limit: -1, Now: time.Now().UnixMilli(),
	})
	if err != nil {
		return false, false, fmt.Errorf("list local memory: %w", err)
	}
	for i := range hits {
		if hits[i].Row.Content == content {
			return false, false, nil
		}
	}
	if dry {
		return true, false, nil
	}
	rec, err := svc.Propose(ctx, &memory.Proposal{
		Category: memory.Category(it.Category), Scope: it.Scope, Content: content, Origin: memory.OriginImport,
	})
	if err != nil {
		return false, false, fmt.Errorf("import memory: %w", err)
	}
	if rec.Status == memory.StatusRejected {
		return false, true, nil
	}
	return true, false, nil
}
