package main

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/skillstore"
	"github.com/damnbot/damnbot/internal/tools"
)

// skillManager is the read-only half of the composition-root seam for the
// skill tools: it lists and loads the skills the store already approved.
type skillManager struct{ db *ctxdb.Store }

var _ tools.SkillManager = (*skillManager)(nil)

func newSkillManager(db *ctxdb.Store) *skillManager { return &skillManager{db: db} }

func (m *skillManager) ListSkills(ctx context.Context, limit int) ([]tools.SkillInfo, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	rows, err := skillstore.Search(ctx, m.db, "", limit)
	if err != nil {
		return nil, fmt.Errorf("skills list: %w", err)
	}
	out := make([]tools.SkillInfo, len(rows))
	for i := range rows {
		out[i] = tools.SkillInfo{Name: rows[i].Name, Description: rows[i].Description}
	}
	return out, nil
}

func (m *skillManager) ViewSkill(ctx context.Context, name string) (tools.SkillDoc, error) {
	r, found, err := skillstore.ByName(ctx, m.db, name)
	if err != nil {
		return tools.SkillDoc{}, fmt.Errorf("skills view: %w", err)
	}
	if !found {
		return tools.SkillDoc{}, tools.ErrSkillNotFound
	}
	files, truncated, ferr := readSkillFiles(filepath.Dir(r.Path))
	if ferr != nil {
		return tools.SkillDoc{}, ferr
	}
	return tools.SkillDoc{Name: r.Name, Description: r.Description, Body: r.Body, Files: files, Truncated: truncated}, nil
}

// readSkillFiles loads the bounded supporting files of one approved skill.
// SKILL.md is the document itself and is never repeated as a supporting file.
func readSkillFiles(dir string) ([]tools.SkillFile, bool, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, false, fmt.Errorf("skill files: %w", err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			_ = cerr // inspection completed; close errors are not actionable
		}
	}()
	var out []tools.SkillFile
	total := 0
	truncated := false
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() || p == "SKILL.md" || len(out) >= 20 {
			return nil
		}
		fi, ierr := d.Info()
		if ierr != nil {
			return fmt.Errorf("skill file info: %w", ierr)
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		if fi.Size() > 64<<10 || total+int(fi.Size()) > 64<<10 {
			truncated = true
			return fs.SkipAll
		}
		b, rerr := root.ReadFile(p)
		if rerr != nil {
			return fmt.Errorf("read skill file: %w", rerr)
		}
		if !utf8.Valid(b) {
			return nil
		}
		total += len(b)
		out = append(out, tools.SkillFile{Path: p, Content: string(b)})
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("skill files: %w", err)
	}
	return out, truncated, nil
}
