package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/skillstore"
	"github.com/damnbot/damnbot/internal/memory"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/skills"
	"github.com/damnbot/damnbot/internal/tools"
)

type skillStore struct{ db *ctxdb.Store }

var _ skills.Store = skillStore{}

func (s skillStore) Index(ctx context.Context, sk *skills.Skill) (int64, error) {
	return skillstore.Index(ctx, s.db, skillRow(sk), 0) //nolint:wrapcheck // manager.Install adds context
}

func (s skillStore) Quarantine(ctx context.Context, sk *skills.Skill, scan string, rejected bool) (int64, int64, error) {
	return skillstore.Quarantine(ctx, s.db, skillRow(sk), scan, rejected, 0) //nolint:wrapcheck // manager.Install adds context
}

func (s skillStore) Approve(ctx context.Context, memoryID int64) error {
	return skillstore.Approve(ctx, s.db, memoryID, 0) //nolint:wrapcheck // caller is the composition root
}

func (s skillStore) Search(ctx context.Context, text string, k int) ([]skills.Skill, error) {
	rows, err := skillstore.Search(ctx, s.db, text, k)
	if err != nil {
		return nil, fmt.Errorf("skill search: %w", err)
	}
	out := make([]skills.Skill, len(rows))
	for i := range rows {
		out[i] = skillFromRow(&rows[i])
	}
	return out, nil
}

func (s skillStore) Status(ctx context.Context, memoryID int64) (skills.Status, error) {
	st, err := skillstore.Status(ctx, s.db, memoryID)
	return skills.Status(st), err
}

func skillRow(sk *skills.Skill) *skillstore.Row {
	return &skillstore.Row{
		ID: sk.ID, Name: sk.Name, Description: sk.Description, Body: sk.Body, Path: sk.Path,
		SHA256: sk.SHA256, Source: string(sk.Source), MemoryID: sk.MemoryID,
	}
}

func skillFromRow(r *skillstore.Row) skills.Skill {
	return skills.Skill{
		ID: r.ID, Name: r.Name, Description: r.Description, Body: r.Body, Path: r.Path,
		SHA256: r.SHA256, Source: skills.Source(r.Source), MemoryID: r.MemoryID,
	}
}

type skillManager struct {
	db  *ctxdb.Store
	svc *skills.Service
	dir string
}

var _ tools.SkillManager = (*skillManager)(nil)

func newSkillManager(db *ctxdb.Store, configDir string, red *redact.Redactor) (*skillManager, error) {
	dir := filepath.Join(configDir, "skills")
	st := skillStore{db: db}
	svc, err := skills.New(st, skills.Config{
		Redact: func(s string) string { return red.String(s) },
		Scan: func(sk *skills.Skill) (string, string, error) {
			scan := memory.ScanText(red, sk.Description+"\n"+sk.Body)
			b, jerr := scan.JSON()
			return string(scan.Severity), b, jerr
		},
		Known: func(name string) bool {
			list, serr := skillstore.Search(context.Background(), db, name, 50)
			return serr == nil && slices.ContainsFunc(list, func(r skillstore.Row) bool { return r.Name == name })
		},
		Files: func(sk *skills.Skill) (fs.FS, error) {
			skillDir := filepath.Dir(sk.Path)
			if _, statErr := os.Stat(skillDir); statErr != nil {
				return nil, fmt.Errorf("skill directory: %w", statErr)
			}
			return rootedFS{dir: skillDir}, nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("skills: %w", err)
	}
	return &skillManager{db: db, svc: svc, dir: dir}, nil
}

type rootedFS struct{ dir string }

func (r rootedFS) Open(name string) (fs.File, error) {
	root, err := os.OpenRoot(r.dir)
	if err != nil {
		return nil, fmt.Errorf("open skill root: %w", err)
	}
	f, err := root.Open(name)
	if err != nil {
		if cerr := root.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
		return nil, fmt.Errorf("open skill file: %w", err)
	}
	return rootedFile{File: f, root: root}, nil
}

type rootedFile struct {
	*os.File
	root *os.Root
}

func (f rootedFile) Close() error {
	return errors.Join(f.File.Close(), f.root.Close())
}

func (m *skillManager) ListSkills(ctx context.Context, limit int) ([]tools.SkillInfo, error) {
	if limit <= 0 || limit > 100 {
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
	rows, err := skillstore.Search(ctx, m.db, name, 50)
	if err != nil {
		return tools.SkillDoc{}, fmt.Errorf("skills view: %w", err)
	}
	for i := range rows {
		if rows[i].Name == name {
			doc := tools.SkillDoc{Name: rows[i].Name, Description: rows[i].Description, Body: rows[i].Body}
			files, ferr := readSkillFiles(filepath.Dir(rows[i].Path))
			if ferr != nil {
				return tools.SkillDoc{}, ferr
			}
			doc.Files = files
			return doc, nil
		}
	}
	return tools.SkillDoc{}, tools.ErrSkillNotFound
}

func readSkillFiles(dir string) ([]tools.SkillFile, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("skill files: %w", err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			_ = cerr // inspection completed; close errors are not actionable
		}
	}()
	var out []tools.SkillFile
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
		if !fi.Mode().IsRegular() || fi.Size() > 64<<10 {
			return nil
		}
		b, rerr := root.ReadFile(p)
		if rerr != nil {
			return fmt.Errorf("read skill file: %w", rerr)
		}
		if !utf8.Valid(b) {
			return nil
		}
		out = append(out, tools.SkillFile{Path: p, Content: string(b)})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("skill files: %w", err)
	}
	return out, nil
}

func (m *skillManager) ManageSkill(ctx context.Context, c *tools.SkillChange) (tools.SkillResult, error) {
	if err := m.guardChange(ctx, c); err != nil {
		return tools.SkillResult{}, err
	}
	if err := os.MkdirAll(m.dir, 0o700); err != nil {
		return tools.SkillResult{}, fmt.Errorf("skills dir: %w", err)
	}
	root, err := os.OpenRoot(m.dir)
	if err != nil {
		return tools.SkillResult{}, fmt.Errorf("skills dir: %w", err)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			_ = cerr // the action is complete; close errors are not actionable
		}
	}()
	if c.Action == "delete" {
		return m.deleteSkill(ctx, root, c)
	}
	if verr := validateSkillChange(c); verr != nil {
		return tools.SkillResult{}, verr
	}
	doc := buildSkillDoc(c)
	sk, err := skills.Parse([]byte(doc), filepath.Join(m.dir, c.Name, "SKILL.md"), skills.SourceAgent)
	if err != nil {
		return tools.SkillResult{}, &tools.ToolError{Code: tools.CodeInvalidArgs, Message: err.Error()}
	}
	if c.Action == "update" {
		if derr := skillstore.Delete(ctx, m.db, c.Name); derr != nil {
			return tools.SkillResult{}, fmt.Errorf("replace skill row: %w", derr)
		}
	}
	if werr := writeManagedSkill(root, c); werr != nil {
		return tools.SkillResult{}, werr
	}
	inst, ierr := m.svc.Install(ctx, &sk)
	if ierr != nil {
		return tools.SkillResult{}, fmt.Errorf("install skill: %w", ierr)
	}
	return tools.SkillResult{Action: c.Action, Name: c.Name, Status: string(inst.Status), MemoryID: inst.MemoryID}, nil
}

func (m *skillManager) guardChange(ctx context.Context, c *tools.SkillChange) error {
	if !validSkillName(c.Name) {
		return &tools.ToolError{Code: tools.CodeInvalidArgs, Message: "name must be [a-z0-9][a-z0-9._-]{0,63}"}
	}
	source, found, err := skillstore.SourceOf(ctx, m.db, c.Name)
	if err != nil {
		return fmt.Errorf("skill source: %w", err)
	}
	switch c.Action {
	case "create":
		if found {
			return tools.ErrSkillExists
		}
	case "update", "delete":
		if !found || source != "agent" {
			return tools.ErrSkillNotFound
		}
	default:
		return &tools.ToolError{Code: tools.CodeInvalidArgs, Message: "action must be create, update or delete"}
	}
	return nil
}

func (m *skillManager) deleteSkill(ctx context.Context, root *os.Root, c *tools.SkillChange) (tools.SkillResult, error) {
	if err := skillstore.Delete(ctx, m.db, c.Name); err != nil {
		if errors.Is(err, skillstore.ErrNotFound) {
			return tools.SkillResult{}, tools.ErrSkillNotFound
		}
		return tools.SkillResult{}, fmt.Errorf("delete skill row: %w", err)
	}
	if err := root.RemoveAll(c.Name); err != nil {
		return tools.SkillResult{}, fmt.Errorf("delete skill: %w", err)
	}
	return tools.SkillResult{Action: c.Action, Name: c.Name, Status: "deleted"}, nil
}

func validateSkillChange(c *tools.SkillChange) error {
	bad := func(msg string) error {
		return &tools.ToolError{Code: tools.CodeInvalidArgs, Message: msg}
	}
	if len(c.Description) > 200 || strings.ContainsAny(c.Description, "\r\n") {
		return bad("description must be a single line of at most 200 bytes")
	}
	if len(c.Body) > 8192 {
		return bad("body exceeds 8192 bytes")
	}
	if len(c.Files) > 20 {
		return bad("at most 20 files")
	}
	for _, f := range c.Files {
		if len(f.Content) > 64<<10 {
			return bad("skill file exceeds 64 KiB")
		}
	}
	for _, e := range c.SideEffects {
		if e == "" || len(e) > 32 {
			return bad("invalid side effect")
		}
		for _, r := range e {
			if r < 'a' || r > 'z' {
				return bad("invalid side effect " + e)
			}
		}
	}
	for _, r := range c.Requires {
		if !validSkillName(r) {
			return bad("invalid required skill " + r)
		}
	}
	return nil
}

func buildSkillDoc(c *tools.SkillChange) string {
	doc := "---\nname: " + c.Name + "\ndescription: " + c.Description + "\n"
	if len(c.SideEffects) > 0 {
		doc += "side_effects: [" + strings.Join(c.SideEffects, ", ") + "]\n"
	}
	if len(c.Requires) > 0 {
		doc += "requires: [" + strings.Join(c.Requires, ", ") + "]\n"
	}
	return doc + "---\n" + c.Body
}

func writeManagedSkill(root *os.Root, c *tools.SkillChange) error {
	dir := c.Name
	if c.Action == "update" {
		if err := root.RemoveAll(dir); err != nil {
			return fmt.Errorf("replace skill: %w", err)
		}
	}
	if err := root.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("skill dir: %w", err)
	}
	if err := writeAtomic(root, filepath.Join(dir, "SKILL.md"), []byte(buildSkillDoc(c))); err != nil {
		return err
	}
	for _, f := range c.Files {
		rel, ok := safeSkillFile(f.Path)
		if !ok {
			return &tools.ToolError{Code: tools.CodeInvalidArgs, Message: "unsafe skill file path " + f.Path}
		}
		full := filepath.Join(dir, rel)
		if err := root.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			return fmt.Errorf("skill file dir: %w", err)
		}
		if err := writeAtomic(root, full, []byte(f.Content)); err != nil {
			return err
		}
	}
	return nil
}

func writeAtomic(root *os.Root, name string, data []byte) error {
	if err := root.WriteFile(name, data, 0o600); err != nil {
		return fmt.Errorf("write skill: %w", err)
	}
	return nil
}

func safeSkillFile(p string) (string, bool) {
	if p == "" || strings.ContainsAny(p, "\\\x00") || path.IsAbs(p) {
		return "", false
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || clean == "SKILL.md" || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return filepath.FromSlash(clean), true
}

var skillNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func validSkillName(s string) bool { return skillNameRE.MatchString(s) }
